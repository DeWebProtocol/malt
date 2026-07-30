package main

import (
	"fmt"
	"strings"
)

const backendArgumentPrefix = "--backend="

func parseStartupBackend(args []string) (string, error) {
	if len(args) != 1 || !strings.HasPrefix(args[0], backendArgumentPrefix) {
		return "", fmt.Errorf("writer requires exactly one %s{kzg|ipa} argument", backendArgumentPrefix)
	}
	backend := strings.TrimPrefix(args[0], backendArgumentPrefix)
	switch backend {
	case "kzg", "ipa":
		return backend, nil
	default:
		return "", fmt.Errorf("unsupported writer backend %q", backend)
	}
}
