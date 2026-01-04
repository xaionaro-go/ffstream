// index_safe.go provides a safe way to access slice elements by index.
package main

func indexSafe[T any](s []T, index int) T {
	if index >= len(s) {
		var zeroValue T
		return zeroValue
	}
	return s[index]
}
