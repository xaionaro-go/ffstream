// ptr.go provides a small utility for creating pointers from literal values.

package ffstream

func ptr[T any](v T) *T {
	return &v
}
