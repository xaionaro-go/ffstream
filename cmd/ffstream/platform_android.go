//go:build android
// +build android

package main

import (
	"github.com/xaionaro-go/ndk/binder"
)

func platformInit() {
	binder.ThreadPoolStart(0)
}
