// errors_test.go validates the package-level sentinel errors are
// distinguishable via errors.Is and survive %w wrapping.

package ffstream

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSentinels_DistinguishableViaErrorsIs(t *testing.T) {
	require.False(t, errors.Is(ErrInputAlreadyExists, ErrInputNotFound),
		"ErrInputAlreadyExists must not match ErrInputNotFound")
	require.False(t, errors.Is(ErrInputNotFound, ErrInputAlreadyExists),
		"ErrInputNotFound must not match ErrInputAlreadyExists")

	wrappedAlready := fmt.Errorf("contextual prefix: %w", ErrInputAlreadyExists)
	require.True(t, errors.Is(wrappedAlready, ErrInputAlreadyExists),
		"wrapped ErrInputAlreadyExists must satisfy errors.Is")
	require.False(t, errors.Is(wrappedAlready, ErrInputNotFound),
		"wrapped ErrInputAlreadyExists must not match ErrInputNotFound")

	wrappedNotFound := fmt.Errorf("contextual prefix: %w", ErrInputNotFound)
	require.True(t, errors.Is(wrappedNotFound, ErrInputNotFound),
		"wrapped ErrInputNotFound must satisfy errors.Is")
	require.False(t, errors.Is(wrappedNotFound, ErrInputAlreadyExists),
		"wrapped ErrInputNotFound must not match ErrInputAlreadyExists")
}
