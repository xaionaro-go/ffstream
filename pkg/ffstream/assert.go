package ffstream

import (
	"context"

	"github.com/facebookincubator/go-belt/tool/logger"
)

func assert(
	ctx context.Context,
	shouldBeTrue bool,
	additionalInfo ...any,
) {
	if shouldBeTrue {
		return
	}

	if len(additionalInfo) == 0 {
		logger.Panic(ctx, "assertion failed")
		return
	}

	logger.Panic(ctx, append([]any{"assertion failed:"}, additionalInfo...)...)
}
