// Command flowy is the host-side Handoff Fabric node. The whole CLI lives in
// internal/flowy; this package is only the binary - it holds the one symbol
// the build must reach from outside, the build stamp, and hands it in.
package main

import (
	"os"

	"github.com/deadtrickster/flowy/internal/flowy"
)

// buildStamp names the build itself, and the build sets it:
//
//	go build -ldflags "-X main.buildStamp=$(git rev-parse --short HEAD)" ./cmd/flowy
//
// A build with no flags, or `go run`, says "src", which is honest rather than
// a commit it is not.
var buildStamp = "src"

func main() {
	os.Exit(flowy.Run(os.Args[1:], buildStamp))
}
