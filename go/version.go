package main

import (
	"fmt"
	"runtime"
)

const (
	Version     = "0.1.0"
	SpecVersion = "aci.loomwork.dev/v0.1"
	AmpVersion  = "0.1"
)

func cmdVersion() {
	fmt.Printf("loomwork %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  spec: %s\n", SpecVersion)
	fmt.Printf("  amp:  %s\n", AmpVersion)
}
