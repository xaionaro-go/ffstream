// errors.go defines package-level sentinel errors for FFStream input management.

package ffstream

import "errors"

var (
	ErrInputAlreadyExists = errors.New("input already exists at this priority")
	ErrInputNotFound      = errors.New("no input at this priority")
)
