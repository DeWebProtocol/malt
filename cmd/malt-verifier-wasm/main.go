//go:build js && wasm

// malt-verifier-wasm exposes the portable client verifier to browser clients.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/dewebprotocol/malt/artifact"
	"github.com/dewebprotocol/malt/protocol"
	clientverifier "github.com/dewebprotocol/malt/sdk/verifier"
)

func main() {
	verifier, initErr := clientverifier.NewDefault()
	artifactFunction := js.FuncOf(func(_ js.Value, args []js.Value) any {
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
	resolveFunction := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if initErr != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ResolveProfile, Error: fmt.Sprintf("initialize verifier: %v", initErr)})
		}
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ResolveProfile, Error: "maltVerifyResolve expects one JSON string"})
		}
		var value protocol.ResolveVerification
		if err := json.Unmarshal([]byte(args[0].String()), &value); err != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ResolveProfile, Error: fmt.Sprintf("decode resolve verification: %v", err)})
		}
		if err := verifier.VerifyResolve(context.Background(), value); err != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ResolveProfile, Error: err.Error()})
		}
		return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ResolveProfile, Valid: true})
	})
	readFunction := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if initErr != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ReadProfile, Error: fmt.Sprintf("initialize verifier: %v", initErr)})
		}
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ReadProfile, Error: "maltVerifyRead expects one JSON string"})
		}
		var value protocol.ReadVerification
		if err := json.Unmarshal([]byte(args[0].String()), &value); err != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ReadProfile, Error: fmt.Sprintf("decode read verification: %v", err)})
		}
		if err := verifier.VerifyRead(context.Background(), value); err != nil {
			return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ReadProfile, Error: err.Error()})
		}
		return encodeProtocolResponse(protocol.VerificationResult{Profile: protocol.ReadProfile, Valid: true})
	})
	js.Global().Set("maltVerifyArtifact", artifactFunction)
	js.Global().Set("maltVerifyResolve", resolveFunction)
	js.Global().Set("maltVerifyRead", readFunction)
	select {}
}

func encodeResponse(response clientverifier.Result) string {
	data, err := json.Marshal(response)
	if err != nil {
		return `{"profile":"malt.artifact/v0alpha2","valid":false,"error":"encode verifier response"}`
	}
	return string(data)
}

func encodeProtocolResponse(response protocol.VerificationResult) string {
	data, err := json.Marshal(response)
	if err != nil {
		return `{"valid":false,"error":"encode verifier response"}`
	}
	return string(data)
}
