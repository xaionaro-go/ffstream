// try.go provides a helper function to ignore errors.

package client

func try[T any](v T, _ error) T {
	return v
}
