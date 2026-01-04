//go:build android
// +build android

package main

// platform_android.go provides Android-specific initialization.

import (
	"github.com/xaionaro-go/ndk/binder"
)

func platformInit() {
	binder.ThreadPoolStart(0)
}
