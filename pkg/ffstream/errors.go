// errors.go defines package-level sentinel errors for FFStream input management.

package ffstream

import "errors"

var (
	ErrInputNotFound = errors.New("no input at this (priority, num)")
)
