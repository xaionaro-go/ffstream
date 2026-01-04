// check_consts.go is a utility to check TCP socket option constants.

// Package main is a utility to check TCP socket option constants.
package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func main() {
	fmt.Println("TCP_THIN_LINEAR_TIMEOUTS:", unix.TCP_THIN_LINEAR_TIMEOUTS)
	fmt.Println("TCP_THIN_DUPACK:", unix.TCP_THIN_DUPACK)
}
