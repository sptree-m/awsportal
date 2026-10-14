package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// NewWebAuthn uses a configured external origin, never Host/Forwarded headers.
func NewWebAuthn(origin string) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("AWSPORTAL_WEBAUTHN_ORIGIN must be an origin without a path")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "localhost") {
		return nil, fmt.Errorf("WebAuthn requires HTTPS (HTTP localhost is for tests only)")
	}
	if net.ParseIP(u.Hostname()) != nil {
		return nil, fmt.Errorf("WebAuthn requires a stable DNS hostname")
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute, TimeoutUVD: 2 * time.Minute}
	return webauthn.New(&webauthn.Config{
		RPID: u.Hostname(), RPDisplayName: "awsportal", RPOrigins: []string{origin}, RPTopOrigins: []string{origin},
		RPTopOriginVerificationMode: protocol.TopOriginImplicitVerificationMode,
		AttestationPreference:       protocol.PreferNoAttestation,
		AuthenticatorSelection:      protocol.AuthenticatorSelection{AuthenticatorAttachment: protocol.Platform, ResidentKey: protocol.ResidentKeyRequirementPreferred, UserVerification: protocol.VerificationRequired},
		Timeouts:                    webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

func NewPasskeyToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// These flags come from authenticator data verified by the WebAuthn library.
func CheckDevicePasskey(c *webauthn.Credential) error {
	if c == nil || !c.Flags.UserPresent || !c.Flags.UserVerified || c.Flags.BackupEligible || c.Flags.BackupState || c.Authenticator.CloneWarning {
		return fmt.Errorf("verified, non-backup-eligible device passkey required")
	}
	return nil
}
