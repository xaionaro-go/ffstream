// try.go provides a helper function to ignore errors in expressions.

package ffstreamserver

func try[T any](v T, _ error) T {
	return v
}
