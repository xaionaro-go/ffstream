package main

import (
	"fmt"

	"github.com/asticode/go-astiav"
)

func printEncoders() {
	codecs := astiav.Codecs()
	for _, codec := range codecs {
		if codec.IsEncoder() {
			printCodec(codec)
		}
	}
}

func printDecoders() {
	codecs := astiav.Codecs()
	for _, codec := range codecs {
		if codec.IsDecoder() {
			printCodec(codec)
		}
	}
}

func printCodec(codec *astiav.Codec) {
	fmt.Printf("%016X %s\n", uint32(codec.ID()), codec.Name())
}
