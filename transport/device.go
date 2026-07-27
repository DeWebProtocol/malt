package transport

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type DeviceLoginStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type DeviceCredential struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DeviceLoginClaim struct {
	Credential  DeviceCredential `json:"credential"`
	TenantID    string           `json:"tenant_id"`
	PrincipalID string           `json:"principal_id"`
}

func (c *Client) StartDeviceLogin(ctx context.Context, name, publicKey string) (*DeviceLoginStart, error) {
	var result DeviceLoginStart
	if err := c.do(ctx, http.MethodPost, "/v1/auth/devices/start", nil, map[string]string{
		"name": strings.TrimSpace(name), "public_key": strings.TrimSpace(publicKey),
	}, &result); err != nil {
		return nil, err
	}
	if result.DeviceCode == "" || result.UserCode == "" || result.VerificationURIComplete == "" ||
		result.ExpiresIn <= 0 || result.Interval <= 0 {
		return nil, fmt.Errorf("gateway returned an invalid device authorization")
	}
	return &result, nil
}

func (c *Client) ClaimDeviceLogin(ctx context.Context, deviceCode, signature string) (*DeviceLoginClaim, error) {
	var result DeviceLoginClaim
	if err := c.do(ctx, http.MethodPost, "/v1/auth/devices/claim", nil, map[string]string{
		"device_code": strings.TrimSpace(deviceCode), "signature": strings.TrimSpace(signature),
	}, &result); err != nil {
		return nil, err
	}
	if result.Credential.ID == "" || result.TenantID == "" || result.PrincipalID == "" {
		return nil, fmt.Errorf("gateway returned an invalid device credential")
	}
	return &result, nil
}
