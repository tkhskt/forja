// Command forja is the user-facing CLI for the network-rules toolchain.
//
// See cmd/ for the cobra command tree and internal/ for the implementation.
package main

import "github.com/tkhskt/forja/cmd"

// version is stamped by the release pipeline via -ldflags "-X main.version=...".
// Defaults to "dev" for local source builds.
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
