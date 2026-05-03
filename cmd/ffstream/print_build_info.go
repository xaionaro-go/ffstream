// print_build_info.go provides functions to print build information.
package main

import (
	"context"
	"io"

	"github.com/xaionaro-go/ffstream/pkg/buildinfo"
)

func printBuildInfo(
	ctx context.Context,
	out io.Writer,
) {
	b, err := buildinfo.JSON()
	assertNoError(ctx, err)
	_, err = out.Write(b)
	assertNoError(ctx, err)
}
