package silero

// Minimal ONNX protobuf decoder.
//
// This file decodes just the subset of the ONNX wire format that the embedded
// Silero VAD graph uses: ModelProto → GraphProto → NodeProto / TensorProto /
// AttributeProto, with float32 and int64 tensor payloads. It is NOT a general
// ONNX parser — unknown fields are skipped, and unsupported tensor data types
// are rejected at load time. Field numbers follow the onnx.proto3 schema
// (https://github.com/onnx/onnx/blob/main/onnx/onnx.proto3).
//
// A hand-rolled decoder keeps the package dependency-free: the alternative,
// generating full onnx.pb.go stubs via google.golang.org/protobuf, would pull
// in ~100 message types to read the five we need.

import (
	"encoding/binary"
	"fmt"
	"math"
)

// onnxModel is the decoded subset of an ONNX ModelProto.
type onnxModel struct {
	graph *onnxGraph
}

// onnxGraph is the decoded subset of an ONNX GraphProto.
type onnxGraph struct {
	nodes        []*onnxNode
	initializers map[string]*onnxTensor
	inputs       []string // graph input names, in declaration order
	outputs      []string // graph output names, in declaration order
}

// onnxNode is the decoded subset of an ONNX NodeProto.
type onnxNode struct {
	name    string
	opType  string
	inputs  []string
	outputs []string
	attrs   map[string]*onnxAttr
}

// onnxAttr is the decoded subset of an ONNX AttributeProto. Which member is
// meaningful is implied by which wire field was populated; the declared
// AttributeType (field 20) is not stored — consumers read the member their op
// contract requires and validate the value.
type onnxAttr struct {
	f      float32
	i      int64
	s      string
	t      *onnxTensor
	g      *onnxGraph
	floats []float32
	ints   []int64
}

// TensorProto.DataType values for the payload types the Silero graph uses.
const (
	tensorFloat = 1
	tensorInt64 = 7
)

// onnxTensor is the decoded subset of an ONNX TensorProto. Exactly one of
// f32/i64 is populated, matching dataType.
type onnxTensor struct {
	name     string
	dims     []int64
	dataType int
	f32      []float32
	i64      []int64
}

// protobuf wire types.
const (
	wireVarint = 0
	wire64Bit  = 1
	wireBytes  = 2
	wire32Bit  = 5
)

// protoReader walks a protobuf-encoded byte slice field by field.
type protoReader struct {
	buf []byte
	pos int
}

func (r *protoReader) done() bool { return r.pos >= len(r.buf) }

// tag reads the next field tag, returning the field number and wire type.
func (r *protoReader) tag() (field int, wire int, err error) {
	v, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 7), nil
}

func (r *protoReader) varint() (uint64, error) {
	var v uint64
	for shift := 0; shift < 64; shift += 7 {
		if r.pos >= len(r.buf) {
			return 0, fmt.Errorf("truncated varint at offset %d", r.pos)
		}
		b := r.buf[r.pos]
		r.pos++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("varint too long at offset %d", r.pos)
}

func (r *protoReader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.buf)-r.pos) {
		return nil, fmt.Errorf("length %d exceeds remaining %d bytes", n, len(r.buf)-r.pos)
	}
	b := r.buf[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b, nil
}

func (r *protoReader) fixed32() (uint32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, fmt.Errorf("truncated fixed32 at offset %d", r.pos)
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *protoReader) fixed64() (uint64, error) {
	if r.pos+8 > len(r.buf) {
		return 0, fmt.Errorf("truncated fixed64 at offset %d", r.pos)
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v, nil
}

// skip discards a field value of the given wire type.
func (r *protoReader) skip(wire int) error {
	switch wire {
	case wireVarint:
		_, err := r.varint()
		return err
	case wire64Bit:
		_, err := r.fixed64()
		return err
	case wireBytes:
		_, err := r.bytes()
		return err
	case wire32Bit:
		_, err := r.fixed32()
		return err
	default:
		return fmt.Errorf("unsupported wire type %d", wire)
	}
}

// parseONNXModel decodes the ModelProto subset from raw bytes.
func parseONNXModel(data []byte) (*onnxModel, error) {
	r := &protoReader{buf: data}
	m := &onnxModel{}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return nil, fmt.Errorf("model: %w", err)
		}
		if field == 7 && wire == wireBytes { // ModelProto.graph
			b, err := r.bytes()
			if err != nil {
				return nil, fmt.Errorf("model.graph: %w", err)
			}
			g, err := parseGraph(b)
			if err != nil {
				return nil, err
			}
			m.graph = g
			continue
		}
		if err := r.skip(wire); err != nil {
			return nil, fmt.Errorf("model field %d: %w", field, err)
		}
	}
	if m.graph == nil {
		return nil, fmt.Errorf("model has no graph")
	}
	return m, nil
}

// parseGraph decodes the GraphProto subset.
func parseGraph(data []byte) (*onnxGraph, error) {
	r := &protoReader{buf: data}
	g := &onnxGraph{initializers: map[string]*onnxTensor{}}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return nil, fmt.Errorf("graph: %w", err)
		}
		switch {
		case field == 1 && wire == wireBytes: // node
			b, err := r.bytes()
			if err != nil {
				return nil, fmt.Errorf("graph.node: %w", err)
			}
			n, err := parseNode(b)
			if err != nil {
				return nil, err
			}
			g.nodes = append(g.nodes, n)
		case field == 5 && wire == wireBytes: // initializer
			b, err := r.bytes()
			if err != nil {
				return nil, fmt.Errorf("graph.initializer: %w", err)
			}
			t, err := parseTensor(b)
			if err != nil {
				return nil, err
			}
			g.initializers[t.name] = t
		case field == 11 && wire == wireBytes: // input (ValueInfoProto)
			b, err := r.bytes()
			if err != nil {
				return nil, fmt.Errorf("graph.input: %w", err)
			}
			name, err := parseValueInfoName(b)
			if err != nil {
				return nil, err
			}
			g.inputs = append(g.inputs, name)
		case field == 12 && wire == wireBytes: // output (ValueInfoProto)
			b, err := r.bytes()
			if err != nil {
				return nil, fmt.Errorf("graph.output: %w", err)
			}
			name, err := parseValueInfoName(b)
			if err != nil {
				return nil, err
			}
			g.outputs = append(g.outputs, name)
		default:
			if err := r.skip(wire); err != nil {
				return nil, fmt.Errorf("graph field %d: %w", field, err)
			}
		}
	}
	return g, nil
}

// parseValueInfoName decodes just the name (field 1) of a ValueInfoProto.
func parseValueInfoName(data []byte) (string, error) {
	r := &protoReader{buf: data}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return "", fmt.Errorf("value_info: %w", err)
		}
		if field == 1 && wire == wireBytes {
			b, err := r.bytes()
			if err != nil {
				return "", fmt.Errorf("value_info.name: %w", err)
			}
			return string(b), nil
		}
		if err := r.skip(wire); err != nil {
			return "", fmt.Errorf("value_info field %d: %w", field, err)
		}
	}
	return "", fmt.Errorf("value_info has no name")
}

// parseNode decodes the NodeProto subset.
func parseNode(data []byte) (*onnxNode, error) {
	r := &protoReader{buf: data}
	n := &onnxNode{attrs: map[string]*onnxAttr{}}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return nil, fmt.Errorf("node: %w", err)
		}
		switch {
		case field == 1 && wire == wireBytes: // input
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			n.inputs = append(n.inputs, string(b))
		case field == 2 && wire == wireBytes: // output
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			n.outputs = append(n.outputs, string(b))
		case field == 3 && wire == wireBytes: // name
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			n.name = string(b)
		case field == 4 && wire == wireBytes: // op_type
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			n.opType = string(b)
		case field == 5 && wire == wireBytes: // attribute
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			name, a, err := parseAttr(b)
			if err != nil {
				return nil, err
			}
			n.attrs[name] = a
		case field == 7 && wire == wireBytes: // domain
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			if d := string(b); d != "" && d != "ai.onnx" {
				return nil, fmt.Errorf("node %q: unsupported op domain %q", n.name, d)
			}
		default:
			if err := r.skip(wire); err != nil {
				return nil, fmt.Errorf("node %q field %d: %w", n.name, field, err)
			}
		}
	}
	return n, nil
}

// parseAttr decodes the AttributeProto subset.
func parseAttr(data []byte) (string, *onnxAttr, error) {
	r := &protoReader{buf: data}
	var name string
	a := &onnxAttr{}
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return "", nil, fmt.Errorf("attribute: %w", err)
		}
		switch {
		case field == 1 && wire == wireBytes: // name
			b, err := r.bytes()
			if err != nil {
				return "", nil, err
			}
			name = string(b)
		case field == 2 && wire == wire32Bit: // f
			v, err := r.fixed32()
			if err != nil {
				return "", nil, err
			}
			a.f = math.Float32frombits(v)
		case field == 3 && wire == wireVarint: // i
			v, err := r.varint()
			if err != nil {
				return "", nil, err
			}
			a.i = int64(v)
		case field == 4 && wire == wireBytes: // s
			b, err := r.bytes()
			if err != nil {
				return "", nil, err
			}
			a.s = string(b)
		case field == 5 && wire == wireBytes: // t
			b, err := r.bytes()
			if err != nil {
				return "", nil, err
			}
			t, err := parseTensor(b)
			if err != nil {
				return "", nil, err
			}
			a.t = t
		case field == 6 && wire == wireBytes: // g
			b, err := r.bytes()
			if err != nil {
				return "", nil, err
			}
			g, err := parseGraph(b)
			if err != nil {
				return "", nil, err
			}
			a.g = g
		case field == 7: // floats (packed or repeated)
			if err := readPackedFloats(r, wire, &a.floats); err != nil {
				return "", nil, err
			}
		case field == 8: // ints (packed or repeated)
			if err := readPackedInts(r, wire, &a.ints); err != nil {
				return "", nil, err
			}
		default:
			if err := r.skip(wire); err != nil {
				return "", nil, fmt.Errorf("attribute %q field %d: %w", name, field, err)
			}
		}
	}
	if name == "" {
		return "", nil, fmt.Errorf("attribute has no name")
	}
	return name, a, nil
}

// parseTensor decodes the TensorProto subset, materialising the payload from
// whichever of raw_data / float_data / int64_data is present.
func parseTensor(data []byte) (*onnxTensor, error) {
	r := &protoReader{buf: data}
	t := &onnxTensor{}
	var rawData []byte
	for !r.done() {
		field, wire, err := r.tag()
		if err != nil {
			return nil, fmt.Errorf("tensor: %w", err)
		}
		switch {
		case field == 1: // dims (packed or repeated varint)
			if err := readPackedInts(r, wire, &t.dims); err != nil {
				return nil, err
			}
		case field == 2 && wire == wireVarint: // data_type
			v, err := r.varint()
			if err != nil {
				return nil, err
			}
			t.dataType = int(v)
		case field == 4: // float_data (packed or repeated fixed32)
			if err := readPackedFloats(r, wire, &t.f32); err != nil {
				return nil, err
			}
		case field == 7: // int64_data (packed or repeated varint)
			if err := readPackedInts(r, wire, &t.i64); err != nil {
				return nil, err
			}
		case field == 8 && wire == wireBytes: // name
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			t.name = string(b)
		case field == 9 && wire == wireBytes: // raw_data
			b, err := r.bytes()
			if err != nil {
				return nil, err
			}
			rawData = b
		default:
			if err := r.skip(wire); err != nil {
				return nil, fmt.Errorf("tensor %q field %d: %w", t.name, field, err)
			}
		}
	}

	if rawData != nil {
		switch t.dataType {
		case tensorFloat:
			if len(rawData)%4 != 0 {
				return nil, fmt.Errorf("tensor %q: raw float data length %d not a multiple of 4", t.name, len(rawData))
			}
			t.f32 = make([]float32, len(rawData)/4)
			for i := range t.f32 {
				t.f32[i] = math.Float32frombits(binary.LittleEndian.Uint32(rawData[i*4:]))
			}
		case tensorInt64:
			if len(rawData)%8 != 0 {
				return nil, fmt.Errorf("tensor %q: raw int64 data length %d not a multiple of 8", t.name, len(rawData))
			}
			t.i64 = make([]int64, len(rawData)/8)
			for i := range t.i64 {
				t.i64[i] = int64(binary.LittleEndian.Uint64(rawData[i*8:]))
			}
		default:
			return nil, fmt.Errorf("tensor %q: unsupported data type %d", t.name, t.dataType)
		}
	}

	// Validate the element count against dims so shape bugs surface at load
	// time, not as index panics mid-inference.
	want := int64(1)
	for _, d := range t.dims {
		want *= d
	}
	var got int64
	switch t.dataType {
	case tensorFloat:
		got = int64(len(t.f32))
	case tensorInt64:
		got = int64(len(t.i64))
	default:
		return nil, fmt.Errorf("tensor %q: unsupported data type %d", t.name, t.dataType)
	}
	if got != want {
		return nil, fmt.Errorf("tensor %q: dims %v imply %d elements, payload has %d", t.name, t.dims, want, got)
	}
	return t, nil
}

// readPackedInts appends int64 values from either a packed (length-delimited)
// or a single unpacked varint field occurrence.
func readPackedInts(r *protoReader, wire int, dst *[]int64) error {
	switch wire {
	case wireBytes:
		b, err := r.bytes()
		if err != nil {
			return err
		}
		pr := &protoReader{buf: b}
		for !pr.done() {
			v, err := pr.varint()
			if err != nil {
				return err
			}
			*dst = append(*dst, int64(v))
		}
		return nil
	case wireVarint:
		v, err := r.varint()
		if err != nil {
			return err
		}
		*dst = append(*dst, int64(v))
		return nil
	default:
		return fmt.Errorf("unsupported wire type %d for int64 field", wire)
	}
}

// readPackedFloats appends float32 values from either a packed
// (length-delimited) or a single unpacked fixed32 field occurrence.
func readPackedFloats(r *protoReader, wire int, dst *[]float32) error {
	switch wire {
	case wireBytes:
		b, err := r.bytes()
		if err != nil {
			return err
		}
		if len(b)%4 != 0 {
			return fmt.Errorf("packed float data length %d not a multiple of 4", len(b))
		}
		for i := 0; i+4 <= len(b); i += 4 {
			*dst = append(*dst, math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
		}
		return nil
	case wire32Bit:
		v, err := r.fixed32()
		if err != nil {
			return err
		}
		*dst = append(*dst, math.Float32frombits(v))
		return nil
	default:
		return fmt.Errorf("unsupported wire type %d for float field", wire)
	}
}
