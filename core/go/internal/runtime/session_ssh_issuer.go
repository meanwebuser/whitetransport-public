package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	"github.com/meanwebuser/whitetransport/core/internal/sessionssh"
)

// issuedSessionSSH is the runtime-owned view of one isolated sshd lease.
// RevokeFunc terminates the session process, including established channels.
type issuedSessionSSH struct {
	Endpoint   carriers.Endpoint
	Profile    session.EgressProfile
	ExpiresAt  time.Time
	RevokeFunc func(context.Context) error
}

func (i *issuedSessionSSH) Revoke(ctx context.Context) error {
	if i == nil || i.RevokeFunc == nil {
		return nil
	}
	return i.RevokeFunc(ctx)
}

type sessionSSHIssuer interface {
	Issue(context.Context, string, time.Duration) (*issuedSessionSSH, error)
	Close(context.Context) error
}

type openSSHSessionIssuer struct {
	issuer *sessionssh.Issuer
}

func newOpenSSHSessionIssuer(cfg config.SessionSSHConfig) (sessionSSHIssuer, error) {
	issuer, err := sessionssh.New(sessionssh.Config{
		BaseDir:             cfg.BaseDir,
		SSHDPath:            cfg.SSHDPath,
		Username:            cfg.Username,
		ListenHost:          cfg.ListenHost,
		AdvertiseHost:       cfg.AdvertiseHost,
		PortMin:             cfg.PortMin,
		PortMax:             cfg.PortMax,
		HostKeyFiles:        append([]string(nil), cfg.HostKeyFiles...),
		DefaultTTL:          time.Duration(cfg.TTLSeconds) * time.Second,
		StartupTimeout:      time.Duration(cfg.StartupTimeoutSecs) * time.Second,
		AllowWildcardListen: cfg.AllowWildcardListen,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize session SSH issuer: %w", err)
	}
	return &openSSHSessionIssuer{issuer: issuer}, nil
}

func (i *openSSHSessionIssuer) Issue(ctx context.Context, sessionID string, ttl time.Duration) (*issuedSessionSSH, error) {
	lease, err := i.issuer.Issue(ctx, sessionssh.IssueRequest{SessionID: sessionID, TTL: ttl})
	if err != nil {
		return nil, err
	}
	endpointIdentity := sha256.Sum256([]byte(sessionID))
	endpoint := carriers.Endpoint{
		ID:      fmt.Sprintf("ssh-session-%x", endpointIdentity[:8]),
		Carrier: carriers.CarrierSSHTCP,
		Address: lease.Address,
	}
	profile := session.EgressProfile{
		Version: session.EgressProfileVersion, EndpointID: endpoint.ID, Carrier: endpoint.Carrier,
		SSH: &session.SSHEgressProfile{
			Username: lease.Username, PrivateKey: lease.PrivateKey,
			HostKeys: append([]string(nil), lease.HostPublicKeys...), ServerAliveIntervalSecs: 5,
		},
	}
	return &issuedSessionSSH{Endpoint: endpoint, Profile: profile, ExpiresAt: lease.ExpiresAt, RevokeFunc: lease.Revoke}, nil
}

func (i *openSSHSessionIssuer) Close(ctx context.Context) error {
	if i == nil || i.issuer == nil {
		return nil
	}
	return i.issuer.Close(ctx)
}

func (c *ControlPlane) issueSessionSSHEgress(ctx context.Context, sessionID string, expiresAt time.Time, encryptedDelivery bool) (*issuedSessionSSH, error) {
	if c.sessionSSHIssuer == nil || !encryptedDelivery {
		return nil, nil
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil, fmt.Errorf("session SSH expiry must be in the future")
	}
	issued, err := c.sessionSSHIssuer.Issue(ctx, sessionID, ttl)
	if err != nil {
		return nil, fmt.Errorf("issue session SSH egress: %w", err)
	}
	c.mu.Lock()
	previous := c.nodeSessionSSH
	c.nodeSessionSSH = issued
	c.mu.Unlock()
	if previous != nil {
		_ = previous.Revoke(context.Background())
	}
	return issued, nil
}
