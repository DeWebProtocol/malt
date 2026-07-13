//go:build js && wasm

// malt-verifier-wasm exposes the portable client verifier to browser clients.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/dewebprotocol/malt/artifact"
	clientverifier "github.com/dewebprotocol/malt/sdk/verifier"
)

func main() {
	verifier, initErr := clientverifier.NewDefault()
	function := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if initErr != nil {
			return encodeResponse(clientverifier.Result{Profile: artifact.Profile, Error: fmt.Sprintf("initialize verifier: %v", initErr)})
		}
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return encodeResponse(clientverifier.Result{Profile: artifact.Profile, Error: "maltVerifyArtifact expects one JSON string"})
		}
		var request clientverifier.Request
		if err := json.Unmarshal([]byte(args[0].String()), &request); err != nil {
			return encodeResponse(clientverifier.Result{Profile: artifact.Profile, Error: fmt.Sprintf("decode verify request: %v", err)})
		}
		if err := verifier.Verify(context.Background(), request); err != nil {
			return encodeResponse(clientverifier.Result{Profile: artifact.Profile, Error: err.Error()})
		}
		return encodeResponse(clientverifier.Result{Profile: artifact.Profile, Valid: true})
	})
	js.Global().Set("maltVerifyArtifact", function)
	select {}
}

func encodeResponse(response clientverifier.Result) string {
	data, err := json.Marshal(response)
	if err != nil {
		return `{"profile":"malt.artifact/v0alpha2","valid":false,"error":"encode verifier response"}`
	}
	return string(data)
}
