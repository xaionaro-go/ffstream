//go:build android
// +build android

package main

// platform_android.go provides Android-specific initialization.

import (
	"github.com/AndroidGoLab/ndk/binderprocess"
)

func platformInit() {
	binderprocess.StartThreadPool(0)
}
