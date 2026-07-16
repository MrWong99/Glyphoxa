package silero

// Bespoke forward pass of the Silero VAD v5 "op18 ifless" ONNX graph (#468).
//
// The embedded model contains a single top-level If node whose two branches
// are the complete 16 kHz and 8 kHz networks: a fully static feed-forward
// graph (STFT-as-Conv → magnitude → 4 Conv+ReLU encoder → decomposed LSTM
// cell → 1×1 Conv → Sigmoid → mean). Because the sample rate is fixed per
// session, compileProgram selects the branch once at load time and compiles
// only it — no control flow, no dynamic shapes, no ONNX Runtime.
//
// The compiler walks the branch's nodes (ONNX graphs are topologically
// sorted), infers every intermediate shape from the fixed input size,
// preallocates all buffers, and emits one closure per node. Executing a frame
// is then a straight run through the closures with zero allocations. Pure
// reshape ops (Squeeze/Unsqueeze) become views on the producer's buffer and
// cost nothing at run time.

import (
	"fmt"
	"math"
)

// tensor is a dense row-major float32 (or, for shape/index constants, int64)
// tensor. Exactly one of f/i is non-nil.
type tensor struct {
	shape []int
	f     []float32
	i     []int64
}

// newTensor allocates a zeroed float32 tensor of the given shape.
func newTensor(shape ...int) *tensor {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return &tensor{shape: shape, f: make([]float32, n)}
}

// elems returns the total element count.
func (t *tensor) elems() int {
	n := 1
	for _, d := range t.shape {
		n *= d
	}
	return n
}

// stft basis initializer names identify which If branch holds which sample
// rate: only the 16 kHz network references the 256-window basis, only the
// 8 kHz network the 128-window one. Selecting by referenced weights is robust
// against upstream reordering the branches in a future export.
const (
	stftBasis16k = "model.stft.forward_basis_buffer"
	stftBasis8k  = "model_8k.stft.forward_basis_buffer"
)

// program is a compiled, ready-to-run forward pass for one sample rate and
// one fixed input length.
type program struct {
	input  *tensor // [1, contextSize+chunkSize], written before each run
	state  *tensor // [2, 1, 128] LSTM (h, c) state, fed back between runs
	output *tensor // [1, 1] speech probability
	stateN *tensor // [2, 1, 128] next LSTM state
	steps  []func()
}

// run executes one forward pass and feeds stateN back into state.
func (p *program) run() {
	for _, step := range p.steps {
		step()
	}
	copy(p.state.f, p.stateN.f)
}

// reset zeroes the recurrent LSTM state.
func (p *program) reset() {
	clear(p.state.f)
}

// compileProgram builds an executable forward pass from the parsed model for
// the given sample rate (8000 or 16000) and model input length (context +
// chunk samples).
func compileProgram(model *onnxModel, sampleRate, inputLen int) (*program, error) {
	branch, err := selectBranch(model.graph, sampleRate)
	if err != nil {
		return nil, err
	}

	c := &compiler{env: map[string]*tensor{}}

	// Outer-scope values visible to the branch subgraph: the top-level graph
	// inputs (input, state; sr is resolved by the branch selection) and every
	// top-level initializer (the weights).
	for name, init := range model.graph.initializers {
		t, err := initTensor(init)
		if err != nil {
			return nil, err
		}
		c.env[name] = t
	}
	input := newTensor(1, inputLen)
	state := newTensor(2, 1, 128)
	c.env["input"] = input
	c.env["state"] = state

	for _, node := range branch.nodes {
		if err := c.compileNode(node); err != nil {
			return nil, fmt.Errorf("silero graph: node %q (%s): %w", node.name, node.opType, err)
		}
	}

	if len(branch.outputs) != 2 {
		return nil, fmt.Errorf("silero graph: branch has %d outputs, want 2 (output, stateN)", len(branch.outputs))
	}
	output, ok := c.env[branch.outputs[0]]
	if !ok {
		return nil, fmt.Errorf("silero graph: branch output %q not produced", branch.outputs[0])
	}
	stateN, ok := c.env[branch.outputs[1]]
	if !ok {
		return nil, fmt.Errorf("silero graph: branch output %q not produced", branch.outputs[1])
	}
	if output.elems() != 1 {
		return nil, fmt.Errorf("silero graph: probability output has shape %v, want a single element", output.shape)
	}
	if stateN.elems() != stateSize {
		return nil, fmt.Errorf("silero graph: stateN has shape %v, want %d elements", stateN.shape, stateSize)
	}

	return &program{input: input, state: state, output: output, stateN: stateN, steps: c.steps}, nil
}

// selectBranch locates the top-level If node and returns the branch subgraph
// for the requested sample rate, identified by which STFT basis it references.
func selectBranch(g *onnxGraph, sampleRate int) (*onnxGraph, error) {
	wantBasis := stftBasis16k
	if sampleRate == 8000 {
		wantBasis = stftBasis8k
	}
	for _, node := range g.nodes {
		if node.opType != "If" {
			continue
		}
		for _, attrName := range []string{"then_branch", "else_branch"} {
			attr, ok := node.attrs[attrName]
			if !ok || attr.g == nil {
				return nil, fmt.Errorf("silero graph: If node %q missing %s", node.name, attrName)
			}
			for _, n := range attr.g.nodes {
				for _, in := range n.inputs {
					if in == wantBasis {
						return attr.g, nil
					}
				}
			}
		}
		return nil, fmt.Errorf("silero graph: neither If branch references %s — model layout changed?", wantBasis)
	}
	return nil, fmt.Errorf("silero graph: no top-level If node found — model layout changed?")
}

// initTensor converts a parsed initializer into a runtime tensor.
func initTensor(t *onnxTensor) (*tensor, error) {
	shape := make([]int, len(t.dims))
	for i, d := range t.dims {
		shape[i] = int(d)
	}
	switch t.dataType {
	case tensorFloat:
		return &tensor{shape: shape, f: t.f32}, nil
	case tensorInt64:
		return &tensor{shape: shape, i: t.i64}, nil
	default:
		return nil, fmt.Errorf("silero graph: initializer %q has unsupported data type %d", t.name, t.dataType)
	}
}

// compiler accumulates the execution plan while walking the graph.
type compiler struct {
	env   map[string]*tensor
	steps []func()
}

// in resolves the i-th input tensor of a node.
func (c *compiler) in(node *onnxNode, i int) (*tensor, error) {
	if i >= len(node.inputs) {
		return nil, fmt.Errorf("missing input %d", i)
	}
	t, ok := c.env[node.inputs[i]]
	if !ok {
		return nil, fmt.Errorf("input %q not defined", node.inputs[i])
	}
	return t, nil
}

// out registers the (single) output tensor of a node.
func (c *compiler) out(node *onnxNode, t *tensor) error {
	if len(node.outputs) != 1 {
		return fmt.Errorf("expected 1 output, node has %d", len(node.outputs))
	}
	c.env[node.outputs[0]] = t
	return nil
}

// intAttr returns an int attribute, or def if absent.
func intAttr(node *onnxNode, name string, def int64) int64 {
	if a, ok := node.attrs[name]; ok {
		return a.i
	}
	return def
}

// intsAttr returns an int-list attribute, or nil if absent.
func intsAttr(node *onnxNode, name string) []int64 {
	if a, ok := node.attrs[name]; ok {
		return a.ints
	}
	return nil
}

// intsInput reads an int64 constant input (e.g. axes, pads, starts).
func (c *compiler) intsInput(node *onnxNode, i int) ([]int64, error) {
	t, err := c.in(node, i)
	if err != nil {
		return nil, err
	}
	if t.i == nil {
		return nil, fmt.Errorf("input %q is not an int64 constant", node.inputs[i])
	}
	return t.i, nil
}

// compileNode dispatches on op type, allocating outputs and appending steps.
func (c *compiler) compileNode(node *onnxNode) error {
	switch node.opType {
	case "Pad":
		return c.compilePad(node)
	case "Unsqueeze":
		return c.compileUnsqueeze(node)
	case "Squeeze":
		return c.compileSqueeze(node)
	case "Conv":
		return c.compileConv(node)
	case "Slice":
		return c.compileSlice(node)
	case "Pow":
		return c.compilePow(node)
	case "Add":
		return c.compileBinary(node, func(a, b float32) float32 { return a + b })
	case "Mul":
		return c.compileBinary(node, func(a, b float32) float32 { return a * b })
	case "Sqrt":
		return c.compileUnary(node, func(x float32) float32 { return float32(math.Sqrt(float64(x))) })
	case "Relu":
		return c.compileUnary(node, func(x float32) float32 {
			if x > 0 {
				return x
			}
			return 0
		})
	case "Sigmoid":
		return c.compileUnary(node, func(x float32) float32 {
			return float32(1 / (1 + math.Exp(-float64(x))))
		})
	case "Tanh":
		return c.compileUnary(node, func(x float32) float32 { return float32(math.Tanh(float64(x))) })
	case "Gather":
		return c.compileGather(node)
	case "Gemm":
		return c.compileGemm(node)
	case "Split":
		return c.compileSplit(node)
	case "Concat":
		return c.compileConcat(node)
	case "ReduceMean":
		return c.compileReduceMean(node)
	default:
		return fmt.Errorf("unsupported op type %q", node.opType)
	}
}

// compilePad handles Pad with mode=reflect on the last axis of a rank-2
// input, the only configuration the Silero graph uses (right-pad the audio by
// the STFT half-window).
func (c *compiler) compilePad(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	pads, err := c.intsInput(node, 1)
	if err != nil {
		return err
	}
	if mode, ok := node.attrs["mode"]; !ok || mode.s != "reflect" {
		return fmt.Errorf("only reflect padding is supported")
	}
	rank := len(x.shape)
	if rank != 2 || len(pads) != 2*rank {
		return fmt.Errorf("unsupported pad config: shape %v, pads %v", x.shape, pads)
	}
	// pads layout: [begin_0, begin_1, end_0, end_1].
	if pads[0] != 0 || pads[2] != 0 {
		return fmt.Errorf("padding on axis 0 not supported: pads %v", pads)
	}
	padL, padR := int(pads[1]), int(pads[3])
	w := x.shape[1]
	if padL >= w || padR >= w {
		return fmt.Errorf("reflect pad %d/%d exceeds input width %d", padL, padR, w)
	}
	out := newTensor(x.shape[0], padL+w+padR)
	c.steps = append(c.steps, func() {
		dst := out.f
		src := x.f
		for j := 0; j < padL; j++ {
			dst[j] = src[padL-j]
		}
		copy(dst[padL:], src)
		for j := 0; j < padR; j++ {
			dst[padL+w+j] = src[w-2-j]
		}
	})
	return c.out(node, out)
}

// normalizeAxis resolves a possibly-negative axis against a rank.
func normalizeAxis(axis int64, rank int) (int, error) {
	a := int(axis)
	if a < 0 {
		a += rank
	}
	if a < 0 || a >= rank {
		return 0, fmt.Errorf("axis %d out of range for rank %d", axis, rank)
	}
	return a, nil
}

// compileUnsqueeze inserts size-1 axes. Data layout is unchanged, so the
// output is a zero-cost view of the input buffer.
func (c *compiler) compileUnsqueeze(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	axes, err := c.intsInput(node, 1)
	if err != nil {
		return err
	}
	outRank := len(x.shape) + len(axes)
	insert := make([]bool, outRank)
	for _, ax := range axes {
		a, err := normalizeAxis(ax, outRank)
		if err != nil {
			return err
		}
		if insert[a] {
			return fmt.Errorf("duplicate unsqueeze axis %d", a)
		}
		insert[a] = true
	}
	shape := make([]int, 0, outRank)
	src := 0
	for i := range outRank {
		if insert[i] {
			shape = append(shape, 1)
		} else {
			shape = append(shape, x.shape[src])
			src++
		}
	}
	return c.out(node, &tensor{shape: shape, f: x.f})
}

// compileSqueeze removes the listed size-1 axes as a zero-cost view.
func (c *compiler) compileSqueeze(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	axes, err := c.intsInput(node, 1)
	if err != nil {
		return err
	}
	remove := make([]bool, len(x.shape))
	for _, ax := range axes {
		a, err := normalizeAxis(ax, len(x.shape))
		if err != nil {
			return err
		}
		if x.shape[a] != 1 {
			return fmt.Errorf("squeeze axis %d has size %d, want 1", a, x.shape[a])
		}
		remove[a] = true
	}
	shape := make([]int, 0, len(x.shape))
	for i, d := range x.shape {
		if !remove[i] {
			shape = append(shape, d)
		}
	}
	return c.out(node, &tensor{shape: shape, f: x.f})
}

// compileConv handles 1-D convolution: x [1, C, W] ⊛ w [M, C, K] (+ bias [M])
// with symmetric integer pads, unit dilation, and group=1 — the only
// configuration the Silero graph uses.
func (c *compiler) compileConv(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	w, err := c.in(node, 1)
	if err != nil {
		return err
	}
	var bias *tensor
	if len(node.inputs) > 2 {
		if bias, err = c.in(node, 2); err != nil {
			return err
		}
	}
	if len(x.shape) != 3 || x.shape[0] != 1 || len(w.shape) != 3 {
		return fmt.Errorf("unsupported conv shapes: x %v, w %v", x.shape, w.shape)
	}
	if g := intAttr(node, "group", 1); g != 1 {
		return fmt.Errorf("conv group %d not supported", g)
	}
	if d := intsAttr(node, "dilations"); len(d) == 1 && d[0] != 1 {
		return fmt.Errorf("conv dilation %d not supported", d[0])
	}
	strides := intsAttr(node, "strides")
	stride := 1
	if len(strides) == 1 {
		stride = int(strides[0])
	}
	pads := intsAttr(node, "pads")
	padL, padR := 0, 0
	if len(pads) == 2 {
		padL, padR = int(pads[0]), int(pads[1])
	} else if len(pads) != 0 {
		return fmt.Errorf("unsupported conv pads %v", pads)
	}

	channels, width := x.shape[1], x.shape[2]
	outCh, kernel := w.shape[0], w.shape[2]
	if w.shape[1] != channels {
		return fmt.Errorf("conv channel mismatch: x has %d, w expects %d", channels, w.shape[1])
	}
	if bias != nil && bias.elems() != outCh {
		return fmt.Errorf("conv bias has %d elements, want %d", bias.elems(), outCh)
	}
	outW := (width+padL+padR-kernel)/stride + 1
	if outW <= 0 {
		return fmt.Errorf("conv output width %d (input %d, kernel %d, stride %d)", outW, width, kernel, stride)
	}
	out := newTensor(1, outCh, outW)

	c.steps = append(c.steps, func() {
		for m := range outCh {
			wm := w.f[m*channels*kernel : (m+1)*channels*kernel]
			om := out.f[m*outW : (m+1)*outW]
			for ow := range outW {
				start := ow*stride - padL
				k0, k1 := 0, kernel
				if start < 0 {
					k0 = -start
				}
				if start+kernel > width {
					k1 = width - start
				}
				var acc float32
				if bias != nil {
					acc = bias.f[m]
				}
				for ch := range channels {
					xc := x.f[ch*width : (ch+1)*width]
					wc := wm[ch*kernel : (ch+1)*kernel]
					for k := k0; k < k1; k++ {
						acc += wc[k] * xc[start+k]
					}
				}
				om[ow] = acc
			}
		}
	})
	return c.out(node, out)
}

// compileSlice handles Slice over a single axis with step 1, resolving
// starts/ends/axes from int64 constant inputs.
func (c *compiler) compileSlice(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	starts, err := c.intsInput(node, 1)
	if err != nil {
		return err
	}
	ends, err := c.intsInput(node, 2)
	if err != nil {
		return err
	}
	axes, err := c.intsInput(node, 3)
	if err != nil {
		return err
	}
	if len(node.inputs) > 4 {
		steps, err := c.intsInput(node, 4)
		if err != nil {
			return err
		}
		if len(steps) != 1 || steps[0] != 1 {
			return fmt.Errorf("only step 1 slices are supported, got %v", steps)
		}
	}
	if len(starts) != 1 || len(ends) != 1 || len(axes) != 1 {
		return fmt.Errorf("only single-axis slices are supported")
	}
	axis, err := normalizeAxis(axes[0], len(x.shape))
	if err != nil {
		return err
	}
	dim := x.shape[axis]
	start, end := int(starts[0]), dim
	if ends[0] < int64(dim) {
		end = int(ends[0])
	}
	if start < 0 || start >= end || end > dim {
		return fmt.Errorf("slice [%d:%d) out of range for dim %d", start, end, dim)
	}

	outer, inner := 1, 1
	for _, d := range x.shape[:axis] {
		outer *= d
	}
	for _, d := range x.shape[axis+1:] {
		inner *= d
	}
	shape := make([]int, len(x.shape))
	copy(shape, x.shape)
	shape[axis] = end - start
	out := newTensor(shape...)

	span := (end - start) * inner
	c.steps = append(c.steps, func() {
		for o := range outer {
			src := x.f[(o*dim+start)*inner : (o*dim+start)*inner+span]
			copy(out.f[o*span:(o+1)*span], src)
		}
	})
	return c.out(node, out)
}

// compilePow handles Pow with a scalar constant exponent (the graph only
// squares the STFT real/imaginary parts).
func (c *compiler) compilePow(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	exp, err := c.in(node, 1)
	if err != nil {
		return err
	}
	if exp.f == nil || exp.elems() != 1 {
		return fmt.Errorf("only scalar float exponents are supported")
	}
	e := exp.f[0]
	out := newTensor(x.shape...)
	if e == 2 {
		c.steps = append(c.steps, func() {
			for i, v := range x.f {
				out.f[i] = v * v
			}
		})
	} else {
		e64 := float64(e)
		c.steps = append(c.steps, func() {
			for i, v := range x.f {
				out.f[i] = float32(math.Pow(float64(v), e64))
			}
		})
	}
	return c.out(node, out)
}

// sameShape reports whether two shapes are element-wise identical.
func sameShape(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// compileBinary handles element-wise two-input ops on equal shapes (the graph
// never broadcasts Add/Mul).
func (c *compiler) compileBinary(node *onnxNode, op func(a, b float32) float32) error {
	a, err := c.in(node, 0)
	if err != nil {
		return err
	}
	b, err := c.in(node, 1)
	if err != nil {
		return err
	}
	if !sameShape(a.shape, b.shape) {
		return fmt.Errorf("shape mismatch %v vs %v (broadcasting not supported)", a.shape, b.shape)
	}
	out := newTensor(a.shape...)
	c.steps = append(c.steps, func() {
		for i := range out.f {
			out.f[i] = op(a.f[i], b.f[i])
		}
	})
	return c.out(node, out)
}

// compileUnary handles element-wise one-input ops.
func (c *compiler) compileUnary(node *onnxNode, op func(x float32) float32) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	out := newTensor(x.shape...)
	c.steps = append(c.steps, func() {
		for i, v := range x.f {
			out.f[i] = op(v)
		}
	})
	return c.out(node, out)
}

// compileGather handles Gather with a scalar constant index: it selects one
// slice along the axis and drops that axis (encoder last-frame pick and LSTM
// h/c state selection).
func (c *compiler) compileGather(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	idxT, err := c.in(node, 1)
	if err != nil {
		return err
	}
	if idxT.i == nil || idxT.elems() != 1 {
		return fmt.Errorf("only scalar constant indices are supported")
	}
	axis, err := normalizeAxis(intAttr(node, "axis", 0), len(x.shape))
	if err != nil {
		return err
	}
	dim := x.shape[axis]
	idx := int(idxT.i[0])
	if idx < 0 {
		idx += dim
	}
	if idx < 0 || idx >= dim {
		return fmt.Errorf("gather index %d out of range for dim %d", idxT.i[0], dim)
	}

	outer, inner := 1, 1
	for _, d := range x.shape[:axis] {
		outer *= d
	}
	for _, d := range x.shape[axis+1:] {
		inner *= d
	}
	shape := make([]int, 0, len(x.shape)-1)
	shape = append(shape, x.shape[:axis]...)
	shape = append(shape, x.shape[axis+1:]...)
	out := newTensor(shape...)

	c.steps = append(c.steps, func() {
		for o := range outer {
			src := x.f[(o*dim+idx)*inner : (o*dim+idx)*inner+inner]
			copy(out.f[o*inner:(o+1)*inner], src)
		}
	})
	return c.out(node, out)
}

// compileGemm handles Gemm with transA=0, transB=1, alpha=beta=1 — the LSTM
// gate projections: out [1, N] = a [1, K] · bT [N, K]ᵀ + bias [N].
func (c *compiler) compileGemm(node *onnxNode) error {
	a, err := c.in(node, 0)
	if err != nil {
		return err
	}
	b, err := c.in(node, 1)
	if err != nil {
		return err
	}
	bias, err := c.in(node, 2)
	if err != nil {
		return err
	}
	if intAttr(node, "transA", 0) != 0 || intAttr(node, "transB", 0) != 1 {
		return fmt.Errorf("only transA=0, transB=1 is supported")
	}
	if fa, ok := node.attrs["alpha"]; ok && fa.f != 1 {
		return fmt.Errorf("alpha %v not supported", fa.f)
	}
	if fb, ok := node.attrs["beta"]; ok && fb.f != 1 {
		return fmt.Errorf("beta %v not supported", fb.f)
	}
	if len(a.shape) != 2 || a.shape[0] != 1 || len(b.shape) != 2 || a.shape[1] != b.shape[1] {
		return fmt.Errorf("unsupported gemm shapes: a %v, b %v", a.shape, b.shape)
	}
	k, n := a.shape[1], b.shape[0]
	if bias.elems() != n {
		return fmt.Errorf("gemm bias has %d elements, want %d", bias.elems(), n)
	}
	out := newTensor(1, n)
	c.steps = append(c.steps, func() {
		av := a.f
		for row := range n {
			bv := b.f[row*k : (row+1)*k]
			acc := bias.f[row]
			for j := range k {
				acc += av[j] * bv[j]
			}
			out.f[row] = acc
		}
	})
	return c.out(node, out)
}

// compileSplit handles Split into equal parts along an axis whose leading
// dimensions are all 1 (the LSTM gate split [1, 512] → 4 × [1, 128]), so each
// output is a zero-cost view of a contiguous span.
func (c *compiler) compileSplit(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	axis, err := normalizeAxis(intAttr(node, "axis", 0), len(x.shape))
	if err != nil {
		return err
	}
	parts := len(node.outputs)
	if n := intAttr(node, "num_outputs", int64(parts)); int(n) != parts {
		return fmt.Errorf("num_outputs %d does not match %d node outputs", n, parts)
	}
	if len(node.inputs) > 1 {
		return fmt.Errorf("explicit split sizes are not supported")
	}
	for _, d := range x.shape[:axis] {
		if d != 1 {
			return fmt.Errorf("split with non-unit leading dims %v not supported", x.shape)
		}
	}
	dim := x.shape[axis]
	if dim%parts != 0 {
		return fmt.Errorf("cannot split dim %d into %d equal parts", dim, parts)
	}
	inner := 1
	for _, d := range x.shape[axis+1:] {
		inner *= d
	}
	span := dim / parts * inner
	shape := make([]int, len(x.shape))
	copy(shape, x.shape)
	shape[axis] = dim / parts
	for p := range parts {
		partShape := make([]int, len(shape))
		copy(partShape, shape)
		c.env[node.outputs[p]] = &tensor{shape: partShape, f: x.f[p*span : (p+1)*span]}
	}
	return nil
}

// compileConcat handles Concat along axis 0 (stacking the LSTM h and c into
// stateN).
func (c *compiler) compileConcat(node *onnxNode) error {
	if a := intAttr(node, "axis", 0); a != 0 {
		return fmt.Errorf("only axis-0 concat is supported, got %d", a)
	}
	ins := make([]*tensor, len(node.inputs))
	total := 0
	for i := range node.inputs {
		t, err := c.in(node, i)
		if err != nil {
			return err
		}
		if i > 0 && !sameShape(t.shape[1:], ins[0].shape[1:]) {
			return fmt.Errorf("concat input %d shape %v incompatible with %v", i, t.shape, ins[0].shape)
		}
		ins[i] = t
		total += t.shape[0]
	}
	shape := make([]int, len(ins[0].shape))
	copy(shape, ins[0].shape)
	shape[0] = total
	out := newTensor(shape...)
	c.steps = append(c.steps, func() {
		off := 0
		for _, t := range ins {
			copy(out.f[off:], t.f)
			off += len(t.f)
		}
	})
	return c.out(node, out)
}

// compileReduceMean handles ReduceMean over a single axis with keepdims=0
// (the final probability average).
func (c *compiler) compileReduceMean(node *onnxNode) error {
	x, err := c.in(node, 0)
	if err != nil {
		return err
	}
	axes, err := c.intsInput(node, 1)
	if err != nil {
		return err
	}
	if len(axes) != 1 {
		return fmt.Errorf("only single-axis reduce is supported")
	}
	if kd := intAttr(node, "keepdims", 1); kd != 0 {
		return fmt.Errorf("keepdims=%d not supported", kd)
	}
	axis, err := normalizeAxis(axes[0], len(x.shape))
	if err != nil {
		return err
	}
	dim := x.shape[axis]
	outer, inner := 1, 1
	for _, d := range x.shape[:axis] {
		outer *= d
	}
	for _, d := range x.shape[axis+1:] {
		inner *= d
	}
	shape := make([]int, 0, len(x.shape)-1)
	shape = append(shape, x.shape[:axis]...)
	shape = append(shape, x.shape[axis+1:]...)
	out := newTensor(shape...)
	scale := 1 / float32(dim)
	c.steps = append(c.steps, func() {
		for o := range outer {
			for in := range inner {
				var sum float32
				for d := range dim {
					sum += x.f[(o*dim+d)*inner+in]
				}
				out.f[o*inner+in] = sum * scale
			}
		}
	})
	return c.out(node, out)
}
