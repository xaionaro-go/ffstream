//go:build android
// +build android

package main

// platform_android.go provides Android-specific initialization.

import (
	"github.com/xaionaro-go/ndk/binderprocess"
)

func platformInit() {
	binderprocess.StartThreadPool(0)
}
