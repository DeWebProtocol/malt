//go:build !js || !wasm

package main

import "fmt"

func main() {
	fmt.Println("malt-verifier-wasm must be built with GOOS=js GOARCH=wasm")
}
