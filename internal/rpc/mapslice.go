package rpc

// mapSlice maps every element of in through f, preallocating the result. The
// storage→proto list handlers all share this shape; the `make(..., 0, len)`
// matters — an empty input yields an empty (non-nil) slice, which proto JSON
// renders as [] rather than null.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}
