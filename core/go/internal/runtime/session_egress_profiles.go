package runtime

import (
	"fmt"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	"golang.org/x/crypto/ssh"
)

// sessionBindingFromEgressProfile creates an in-memory carrier binding from a
// node-issued encrypted profile. The profile identity must exactly match the
// public endpoint so credentials cannot be replayed onto another route.
func sessionBindingFromEgressProfile(endpoint carriers.Endpoint, profile session.EgressProfile) (policy.CarrierBinding, error) {
	return sessionBindingFromEgressProfileWithRuntime(endpoint, profile, config.SessionEgressConfig{})
}

// sessionBindingFromEgressProfileWithRuntime combines a server-issued remote
// profile with trusted local sidecar settings without persisting either value.
func sessionBindingFromEgressProfileWithRuntime(endpoint carriers.Endpoint, profile session.EgressProfile, localRuntime config.SessionEgressConfig) (policy.CarrierBinding, error) {
	if profile.EndpointID != endpoint.ID || profile.Carrier != endpoint.Carrier {
		return policy.CarrierBinding{}, fmt.Errorf("session profile identity does not match endpoint %s", endpoint.ID)
	}
	if profile.SSH != nil {
		if err := validateSessionSSHProfile(profile.SSH); err != nil {
			return policy.CarrierBinding{}, fmt.Errorf("SSH session profile %s: %w", endpoint.ID, err)
		}
		carrier, err := carriers.NewSSHCarrier(carriers.SSHConfig{
			Username:                profile.SSH.Username,
			PrivateKey:              profile.SSH.PrivateKey,
			PrivateKeyPassphrase:    profile.SSH.PrivateKeyPassphrase,
			HostKeys:                append([]string(nil), profile.SSH.HostKeys...),
			ServerAliveIntervalSecs: profile.SSH.ServerAliveIntervalSecs,
		})
		if err != nil {
			return policy.CarrierBinding{}, fmt.Errorf("create SSH session carrier: %w", err)
		}
		return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
	}
	vlessConfig := carriers.SingBoxVLESSConfig{URI: profile.URI}
	if localRuntime.SingBox != nil {
		vlessConfig.BinaryPath = localRuntime.SingBox.BinaryPath
		vlessConfig.ConfigDir = localRuntime.SingBox.ConfigDir
		vlessConfig.LocalListen = localRuntime.SingBox.LocalListen
		vlessConfig.StartTimeoutSecs = localRuntime.SingBox.StartTimeoutSecs
	}
	carrier, err := carriers.NewSingBoxVLESSCarrier(vlessConfig)
	if err != nil {
		return policy.CarrierBinding{}, fmt.Errorf("create VLESS session carrier: %w", err)
	}
	return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
}

func validateSessionSSHProfile(profile *session.SSHEgressProfile) error {
	privateKey := []byte(profile.PrivateKey)
	passphrase := []byte(profile.PrivateKeyPassphrase)
	defer clear(privateKey)
	defer clear(passphrase)
	var err error
	if len(passphrase) > 0 {
		_, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, passphrase)
	} else {
		_, err = ssh.ParsePrivateKey(privateKey)
	}
	if err != nil {
		return fmt.Errorf("parse inline private key: %w", err)
	}
	for _, raw := range profile.HostKeys {
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw))); err != nil {
			return fmt.Errorf("parse pinned host key: %w", err)
		}
	}
	return nil
}

// portableSSHEgressProfile extracts only the inline, host-key-pinned fields a
// remote client can safely consume without filesystem or TokenStore state.
func portableSSHEgressProfile(carrier *carriers.SSHCarrier) (*session.SSHEgressProfile, error) {
	cfg := carrier.Config()
	profile := &session.SSHEgressProfile{
		Username:                cfg.Username,
		PrivateKey:              cfg.PrivateKey,
		PrivateKeyPassphrase:    cfg.PrivateKeyPassphrase,
		HostKeys:                append([]string(nil), cfg.HostKeys...),
		ServerAliveIntervalSecs: cfg.ServerAliveIntervalSecs,
	}
	if strings.TrimSpace(profile.Username) == "" || strings.TrimSpace(profile.PrivateKey) == "" || len(profile.HostKeys) == 0 {
		return nil, fmt.Errorf("portable inline key, username and pinned host key are required")
	}
	if err := validateSessionSSHProfile(profile); err != nil {
		return nil, err
	}
	return profile, nil
}
