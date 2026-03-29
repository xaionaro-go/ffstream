//go:build linux && !android

package main

import (
	_ "github.com/xaionaro-go/ffstream/pkg/pulse"
)

func platformInit() {}
