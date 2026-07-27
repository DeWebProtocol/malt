package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/deviceauth"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	"github.com/spf13/cobra"
)

var (
	loginGateway   string
	loginDevice    string
	loginNoBrowser bool
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authorize this client device through the Gateway account",
	Args:  cobra.NoArgs,
	RunE:  runLogin,
}

func init() {
	loginCmd.Flags().StringVar(&loginGateway, "gateway", "", "Gateway base URL (defaults to configuration)")
	loginCmd.Flags().StringVar(&loginDevice, "device-name", "", "Device display name")
	loginCmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "Print the authorization URL without opening it")
	rootCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, _ []string) error {
	cfg, configPath, err := loadConfigForUpdate()
	if err != nil {
		return err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(loginGateway), "/")
	if baseURL == "" {
		baseURL = cfg.GatewayBaseURL()
	}
	if err := validateSecureLoginGateway(baseURL); err != nil {
		return err
	}
	deviceName := strings.TrimSpace(loginDevice)
	if deviceName == "" {
		deviceName, err = os.Hostname()
		if err != nil || strings.TrimSpace(deviceName) == "" {
			deviceName = "MALT client"
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate device authorization key: %w", err)
	}
	gateway, err := gatewayclient.New(gatewayclient.Options{BaseURL: baseURL})
	if err != nil {
		return err
	}
	started, err := gateway.StartDeviceLogin(cmd.Context(), deviceName, base64.RawURLEncoding.EncodeToString(publicKey))
	if err != nil {
		return daemonCommandError(err)
	}
	if err := validateDeviceAuthorizationURL(baseURL, started.VerificationURIComplete); err != nil {
		return err
	}
	fmt.Printf("Authorize this device in your browser:\n%s\nCode: %s\n", started.VerificationURIComplete, started.UserCode)
	if !loginNoBrowser {
		if err := openBrowser(started.VerificationURIComplete); err != nil {
			fmt.Printf("Could not open the browser automatically: %v\n", err)
		}
	}
	signature := ed25519.Sign(privateKey, []byte("malt-device-login-v1\x00"+started.DeviceCode))
	timeout := time.Duration(started.ExpiresIn) * time.Second
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	interval := time.Duration(started.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	var claimed *gatewayclient.DeviceLoginClaim
	for {
		claimed, err = gateway.ClaimDeviceLogin(ctx, started.DeviceCode, base64.RawURLEncoding.EncodeToString(signature))
		if err == nil {
			break
		}
		var apiErr *gatewayclient.Error
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != 428 && apiErr.StatusCode != 409) {
			return daemonCommandError(err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("device authorization did not complete before expiry: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
	credential := deviceauth.Credential{
		Version: 2, Gateway: baseURL, Name: deviceName,
		CredentialID: claimed.Credential.ID, TenantID: claimed.TenantID, PrincipalID: claimed.PrincipalID,
		PublicKey:   base64.RawURLEncoding.EncodeToString(publicKey),
		PrivateKey:  base64.RawURLEncoding.EncodeToString(privateKey),
		KeyProvider: "software-file", CreatedAt: time.Now().UTC(),
	}
	if err := (deviceauth.FileProvider{Path: cfg.Gateway.CredentialPath}).Save(credential); err != nil {
		return err
	}
	cfg.Gateway.BaseURL = baseURL
	cfg.Gateway.APIKey = ""
	if err := clientconfig.Write(configPath, cfg); err != nil {
		return err
	}
	printJSON(map[string]any{
		"gateway": baseURL, "device": deviceName, "credential_id": claimed.Credential.ID,
		"tenant_id": claimed.TenantID, "principal_id": claimed.PrincipalID,
	})
	return nil
}

func validateSecureLoginGateway(baseURL string) error {
	value, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || value.Scheme == "" || value.Host == "" {
		return fmt.Errorf("gateway base URL must be absolute")
	}
	if value.Opaque != "" || value.User != nil || value.RawQuery != "" || value.Fragment != "" ||
		(value.Path != "" && value.Path != "/") {
		return fmt.Errorf("gateway base URL must contain only an origin")
	}
	if strings.EqualFold(value.Scheme, "https") {
		return nil
	}
	host := value.Hostname()
	if strings.EqualFold(value.Scheme, "http") && (strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())) {
		return nil
	}
	return fmt.Errorf("device login requires HTTPS or a loopback HTTP Gateway")
}

func validateDeviceAuthorizationURL(baseURL, authorizationURL string) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	target, err := url.Parse(authorizationURL)
	if err != nil || target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) ||
		target.User != nil || target.Fragment != "" {
		return fmt.Errorf("Gateway returned an unsafe device authorization URL")
	}
	return nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
