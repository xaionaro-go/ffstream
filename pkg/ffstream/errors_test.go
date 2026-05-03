// errors_test.go validates the package-level sentinel errors are
// distinguishable via errors.Is and survive %w wrapping.

package ffstream

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrInputNotFound_PreservedThroughWrap(t *testing.T) {
	wrapped := fmt.Errorf("contextual prefix: %w", ErrInputNotFound)
	require.True(t, errors.Is(wrapped, ErrInputNotFound),
		"wrapped ErrInputNotFound must satisfy errors.Is")

	other := errors.New("some unrelated error")
	require.False(t, errors.Is(other, ErrInputNotFound),
		"unrelated error must not match ErrInputNotFound")
}
