package main

import "testing"

func TestDeviceLoginRequiresHTTPSExceptLoopback(t *testing.T) {
	for _, value := range []string{
		"https://gateway.example",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
	} {
		if err := validateSecureLoginGateway(value); err != nil {
			t.Fatalf("%s rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		"http://gateway.example",
		"ftp://gateway.example",
		"gateway.example",
		"https://user@gateway.example",
		"https://gateway.example/api",
		"https://gateway.example?next=attacker.example",
		"https://gateway.example/#fragment",
	} {
		if err := validateSecureLoginGateway(value); err == nil {
			t.Fatalf("%s was accepted", value)
		}
	}
}

func TestDeviceAuthorizationURLMustRemainSameOrigin(t *testing.T) {
	base := "https://gateway.example"
	if err := validateDeviceAuthorizationURL(base, base+"/v1/auth/devices/authorize?device_code=one"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"https://attacker.example/v1/auth/devices/authorize",
		"http://gateway.example/v1/auth/devices/authorize",
		"https://gateway.example@attacker.example/v1/auth/devices/authorize",
	} {
		if err := validateDeviceAuthorizationURL(base, value); err == nil {
			t.Fatalf("unsafe authorization URL %s was accepted", value)
		}
	}
}
