package carriers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuildSingBoxConfigJSON builds the loopback sidecar profile for any managed
// sing-box stream adapter. Protocol-specific fields stay in the adapter config;
// the tunnel only starts the resulting child and never selects a protocol.
func BuildSingBoxConfigJSON(cfg SingBoxVLESSConfig, localListen string) ([]byte, error) {
	listenHost, listenPort, err := SplitHostPort(localListen)
	if err != nil {
		return nil, fmt.Errorf("sing-box tunnel: parse local listen %q: %w", localListen, err)
	}
	outboundType := strings.TrimSpace(cfg.OutboundType)
	if outboundType == "" {
		outboundType = "vless"
	}
	outbound := map[string]any{
		"type":        outboundType,
		"tag":         "proxy",
		"server":      cfg.Server,
		"server_port": cfg.ServerPort,
	}
	switch outboundType {
	case "vless":
		outbound["uuid"] = cfg.UUID
		outbound["network"] = cfg.Network
		if cfg.Flow != "" {
			outbound["flow"] = cfg.Flow
		}
	case "hysteria2":
		outbound["password"] = cfg.Password
		if cfg.HysteriaObfsType != "" {
			obfs := map[string]any{"type": cfg.HysteriaObfsType}
			if cfg.HysteriaObfsPassword != "" {
				obfs["password"] = cfg.HysteriaObfsPassword
			}
			outbound["obfs"] = obfs
		}
	default:
		return nil, fmt.Errorf("sing-box tunnel: unsupported outbound type %q", outboundType)
	}
	if cfg.TLSEnabled {
		tlsConfig := map[string]any{
			"enabled":     true,
			"server_name": cfg.TLSServerName,
			"insecure":    cfg.TLSInsecure,
		}
		if cfg.UTLSFingerprint != "" {
			tlsConfig["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": cfg.UTLSFingerprint,
			}
		}
		if cfg.RealityEnabled {
			tlsConfig["reality"] = map[string]any{"enabled": true, "public_key": cfg.RealityPublicKey, "short_id": cfg.RealityShortID}
		}
		if outboundType == "hysteria2" {
			// Xray's Hysteria 2 listener advertises HTTP/3. Keep the
			// protocol-specific ALPN explicit so managed probes match the
			// accepted server profile instead of relying on sing-box defaults.
			tlsConfig["alpn"] = []string{"h3"}
		}
		outbound["tls"] = tlsConfig
	}
	if cfg.TransportType != "" && !strings.EqualFold(cfg.TransportType, "tcp") {
		transport := map[string]any{"type": cfg.TransportType}
		if cfg.TransportHost != "" && !strings.EqualFold(cfg.TransportType, "grpc") {
			transport["headers"] = map[string]string{"Host": cfg.TransportHost}
		}
		if cfg.TransportPath != "" {
			if strings.EqualFold(cfg.TransportType, "grpc") {
				transport["service_name"] = cfg.TransportPath
			} else {
				transport["path"] = cfg.TransportPath
			}
		}
		outbound["transport"] = transport
	}
	doc := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"inbounds": []map[string]any{{
			"type":        "mixed",
			"tag":         "wt-mixed-in",
			"listen":      listenHost,
			"listen_port": listenPort,
		}},
		"outbounds": []map[string]any{outbound},
		"route":     map[string]any{"final": "proxy"},
	}
	return json.MarshalIndent(doc, "", "  ")
}
