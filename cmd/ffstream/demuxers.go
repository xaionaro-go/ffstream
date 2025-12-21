package main

import (
	"fmt"

	"github.com/asticode/go-astiav"
)

func printDemuxers() {
	inputs := astiav.Demuxers()
	for _, input := range inputs {
		fmt.Printf("%s\n", input.Name())
	}
}
