package client

func try[T any](v T, _ error) T {
	return v
}
