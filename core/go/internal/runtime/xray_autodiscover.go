package runtime

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
)

const (
	defaultXrayConfigPath       = "/usr/local/etc/xray/config.json"
	defaultXraySingBoxBinary    = "/usr/local/bin/sing-box"
	xrayAutoDiscoverEnv         = "WT_XRAY_AUTO_DISCOVER"
	xrayConfigPathEnv           = "WT_XRAY_CONFIG_PATH"
	xrayClientEmailEnv          = "WT_XRAY_CLIENT_EMAIL"
	xrayClientUUIDEnv           = "WT_XRAY_CLIENT_UUID"
	xrayPublicHostEnv           = "WT_XRAY_PUBLIC_HOST"
	xrayPublicPortEnv           = "WT_XRAY_PUBLIC_PORT"
	xrayPublicSNIEnv            = "WT_XRAY_PUBLIC_SNI"
	xrayUTLSFingerprintEnv      = "WT_XRAY_UTLS_FINGERPRINT"
	xraySingBoxBinaryEnv        = "WT_SINGBOX_BINARY"
	xraySingBoxLocalListenEnv   = "WT_SINGBOX_LOCAL_LISTEN"
	xraySingBoxStartTimeoutEnv  = "WT_SINGBOX_START_TIMEOUT_SECS"
	xraySingBoxEndpointIDPrefix = "xray-"
)

type xrayConfigFile struct {
	Inbounds []xrayInbound `json:"inbounds"`
}

type xrayInbound struct {
	Tag            string             `json:"tag"`
	Listen         string             `json:"listen"`
	Port           int                `json:"port"`
	Protocol       string             `json:"protocol"`
	Settings       xrayInboundSetting `json:"settings"`
	StreamSettings xrayStreamSettings `json:"streamSettings"`
}

type xrayInboundSetting struct {
	Clients []xrayClient `json:"clients"`
}

type xrayClient struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Flow  string `json:"flow"`
}

type xrayStreamSettings struct {
	Security                  string                   `json:"security"`
	Network                   string                   `json:"network"`
	HTTPUpgradeSettings       *xrayHTTPUpgradeSettings `json:"httpupgradeSettings"`
	HTTPUpgradeSettingsCompat *xrayHTTPUpgradeSettings `json:"httpUpgradeSettings"`
	WSSettings                *xrayHTTPUpgradeSettings `json:"wsSettings"`
	XHTTPSettings             *xrayHTTPUpgradeSettings `json:"xhttpSettings"`
	GRPCSettings              *xrayGRPCSettings        `json:"grpcSettings"`
	RealitySettings           *xrayRealitySettings     `json:"realitySettings"`
}

type xrayGRPCSettings struct {
	ServiceName string `json:"serviceName"`
}
type xrayRealitySettings struct {
	PrivateKey  string   `json:"privateKey"`
	ServerNames []string `json:"serverNames"`
	ShortIDs    []string `json:"shortIds"`
}

type xrayHTTPUpgradeSettings struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

// addAutoDiscoveredXraySingBox augments node configs with a sing-box VLESS
// binding when a local Xray VLESS/HTTPUpgrade inbound is present.
func addAutoDiscoveredXraySingBox(cfg config.Config) config.Config {
	if cfg.Role != config.RoleNode || !xrayAutoDiscoverEnabled() {
		return cfg
	}
	carrierConfigs, err := discoverXraySingBoxCarrierConfigs()
	if err != nil {
		log.Printf("[runtime] xray auto-discovery skipped: %v", err)
		return cfg
	}
	if len(carrierConfigs) == 0 {
		return cfg
	}
	if !enabledCarrierContains(cfg.EnabledCarriers, carriers.CarrierSingBoxVLESS) {
		cfg.EnabledCarriers = append(cfg.EnabledCarriers, carriers.CarrierSingBoxVLESS)
	}
	for _, carrierConfig := range carrierConfigs {
		if hasCarrierConfigID(cfg.CarrierConfigs, carrierConfig.ID) {
			continue
		}
		cfg.CarrierConfigs = append(cfg.CarrierConfigs, carrierConfig)
		log.Printf("[runtime] xray auto-discovered singbox.vless binding=%s endpoint=%s tag=%s", carrierConfig.ID, carrierConfig.Endpoint.Address, carrierConfig.Endpoint.Metadata["xray_tag"])
	}
	return cfg
}

func xrayAutoDiscoverEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(xrayAutoDiscoverEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func hasCarrierConfigID(configs []config.CarrierConfig, id string) bool {
	for _, carrierConfig := range configs {
		if strings.TrimSpace(carrierConfig.ID) == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

func enabledCarrierContains(enabled []string, carrierID string) bool {
	for _, id := range enabled {
		if strings.TrimSpace(id) == carrierID {
			return true
		}
	}
	return false
}

func discoverXraySingBoxCarrierConfigs() ([]config.CarrierConfig, error) {
	cfgPath := strings.TrimSpace(os.Getenv(xrayConfigPathEnv))
	if cfgPath == "" {
		cfgPath = defaultXrayConfigPath
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	var parsed xrayConfigFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	configs := make([]config.CarrierConfig, 0, len(parsed.Inbounds))
	for _, inbound := range parsed.Inbounds {
		if !inbound.isSupportedVLESS() {
			continue
		}
		carrierConfig, err := xrayInboundToSingBoxConfig(inbound)
		if err != nil {
			log.Printf("[runtime] xray inbound %s skipped: %v", safeXrayTag(inbound.Tag), err)
			continue
		}
		configs = append(configs, carrierConfig)
	}
	return configs, nil
}

func (s xrayStreamSettings) httpUpgrade() *xrayHTTPUpgradeSettings {
	if s.HTTPUpgradeSettings != nil {
		return s.HTTPUpgradeSettings
	}
	return s.HTTPUpgradeSettingsCompat
}

func (i xrayInbound) isSupportedVLESS() bool {
	if !strings.EqualFold(strings.TrimSpace(i.Protocol), "vless") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(i.StreamSettings.Network)) {
	case "httpupgrade", "ws", "xhttp", "grpc":
		return true
	case "tcp":
		return strings.EqualFold(strings.TrimSpace(i.StreamSettings.Security), "reality")
	default:
		return false
	}
}

func xrayInboundToSingBoxConfig(inbound xrayInbound) (config.CarrierConfig, error) {
	network := strings.ToLower(strings.TrimSpace(inbound.StreamSettings.Network))
	if network == "tcp" {
		return xrayRealityToSingBoxConfig(inbound)
	}
	transportHost, transportPath := xrayTransportSettings(inbound.StreamSettings, network)
	publicListener := strings.TrimSpace(inbound.Listen) != "" && strings.TrimSpace(inbound.Listen) != "127.0.0.1" && strings.TrimSpace(inbound.Listen) != "::1"
	publicHost := firstNonEmpty(os.Getenv(xrayPublicHostEnv), transportHost)
	if publicHost == "" {
		if listen := strings.TrimSpace(inbound.Listen); listen != "" && listen != "127.0.0.1" && listen != "::1" {
			publicHost = listen
		}
	}
	if publicHost == "" {
		return config.CarrierConfig{}, fmt.Errorf("public host is required; set %s", xrayPublicHostEnv)
	}
	publicPort, err := xrayPublicPort(inbound.Port, publicListener)
	if err != nil {
		return config.CarrierConfig{}, err
	}
	clientUUID := strings.TrimSpace(os.Getenv(xrayClientUUIDEnv))
	if clientUUID == "" {
		clientUUID, err = xraySelectedClientUUID(inbound.Settings.Clients, os.Getenv(xrayClientEmailEnv))
		if err != nil {
			return config.CarrierConfig{}, err
		}
	} else if _, err = xrayExplicitClient(inbound.Settings.Clients, clientUUID); err != nil {
		return config.CarrierConfig{}, err
	}
	publicSNI := firstNonEmpty(os.Getenv(xrayPublicSNIEnv), publicHost)
	fingerprint := firstNonEmpty(os.Getenv(xrayUTLSFingerprintEnv), "chrome")
	transportPath = firstNonEmpty(transportPath, "/")
	vlessURI := buildXrayVLESSURIForTransport(clientUUID, publicHost, publicPort, publicSNI, fingerprint, network, transportHost, transportPath)
	binaryPath := firstNonEmpty(os.Getenv(xraySingBoxBinaryEnv), defaultXraySingBoxBinary)
	localListen := firstNonEmpty(os.Getenv(xraySingBoxLocalListenEnv), "127.0.0.1:0")
	startTimeoutSecs, err := optionalEnvInt(xraySingBoxStartTimeoutEnv)
	if err != nil {
		return config.CarrierConfig{}, err
	}
	tag := safeXrayTag(inbound.Tag)
	return config.CarrierConfig{
		ID: xraySingBoxEndpointIDPrefix + tag,
		Endpoint: config.EndpointConfig{
			ID:      xraySingBoxEndpointIDPrefix + tag,
			Address: net.JoinHostPort(publicHost, strconv.Itoa(publicPort)),
			Metadata: map[string]string{
				"auto_discovered": "xray",
				"xray_tag":        tag,
				"xray_network":    network,
			},
		},
		SingBox: &config.SingBoxConfig{
			URI:              vlessURI,
			BinaryPath:       binaryPath,
			TransportType:    network,
			TransportHost:    transportHost,
			TransportPath:    transportPath,
			LocalListen:      localListen,
			StartTimeoutSecs: startTimeoutSecs,
		},
	}, nil
}

func xrayRealityToSingBoxConfig(inbound xrayInbound) (config.CarrierConfig, error) {
	settings := inbound.StreamSettings.RealitySettings
	if settings == nil || strings.TrimSpace(settings.PrivateKey) == "" {
		return config.CarrierConfig{}, fmt.Errorf("reality private key is required")
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(settings.PrivateKey))
	if err != nil {
		return config.CarrierConfig{}, fmt.Errorf("decode reality private key: %w", err)
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return config.CarrierConfig{}, fmt.Errorf("load reality private key: %w", err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	host := firstNonEmpty(os.Getenv(xrayPublicHostEnv), strings.TrimSpace(inbound.Listen))
	if host == "" || host == "127.0.0.1" {
		return config.CarrierConfig{}, fmt.Errorf("public host is required for reality")
	}
	port, err := xrayPublicPort(inbound.Port, true)
	if err != nil {
		return config.CarrierConfig{}, err
	}
	uuid := strings.TrimSpace(os.Getenv(xrayClientUUIDEnv))
	var selectedClient xrayClient
	if uuid == "" {
		selectedClient, err = xraySelectedClient(inbound.Settings.Clients, os.Getenv(xrayClientEmailEnv))
		if err != nil {
			return config.CarrierConfig{}, err
		}
		uuid = selectedClient.ID
	} else {
		selectedClient, err = xrayExplicitClient(inbound.Settings.Clients, uuid)
		if err != nil {
			return config.CarrierConfig{}, err
		}
	}
	// Reality validates SNI against this inbound's serverNames. The generic
	// WT_XRAY_PUBLIC_SNI applies to TLS reverse-proxy inbounds only and would
	// create a profile that can reach the port but never authenticate.
	sni := firstNonEmpty(firstString(settings.ServerNames), host)
	query := url.Values{}
	query.Set("type", "tcp")
	query.Set("security", "reality")
	query.Set("sni", sni)
	query.Set("fp", firstNonEmpty(os.Getenv(xrayUTLSFingerprintEnv), "chrome"))
	query.Set("pbk", publicKey)
	if flow := strings.TrimSpace(selectedClient.Flow); flow != "" {
		query.Set("flow", flow)
	}
	if sid := firstString(settings.ShortIDs); sid != "" {
		query.Set("sid", sid)
	}
	uri := (&url.URL{Scheme: "vless", User: url.User(uuid), Host: net.JoinHostPort(host, strconv.Itoa(port)), RawQuery: query.Encode()}).String()
	tag := safeXrayTag(inbound.Tag)
	return config.CarrierConfig{ID: xraySingBoxEndpointIDPrefix + tag, Endpoint: config.EndpointConfig{ID: xraySingBoxEndpointIDPrefix + tag, Address: net.JoinHostPort(host, strconv.Itoa(port)), Metadata: map[string]string{"auto_discovered": "xray", "xray_tag": tag, "xray_network": "reality"}}, SingBox: &config.SingBoxConfig{URI: uri, BinaryPath: firstNonEmpty(os.Getenv(xraySingBoxBinaryEnv), defaultXraySingBoxBinary), TransportType: "tcp", LocalListen: firstNonEmpty(os.Getenv(xraySingBoxLocalListenEnv), "127.0.0.1:0")}}, nil
}
func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func xrayTransportSettings(settings xrayStreamSettings, network string) (string, string) {
	switch network {
	case "httpupgrade":
		if value := settings.httpUpgrade(); value != nil {
			return value.Host, value.Path
		}
	case "ws":
		if settings.WSSettings != nil {
			return settings.WSSettings.Host, settings.WSSettings.Path
		}
	case "xhttp":
		if settings.XHTTPSettings != nil {
			return settings.XHTTPSettings.Host, settings.XHTTPSettings.Path
		}
	case "grpc":
		if settings.GRPCSettings != nil {
			return "", strings.TrimLeft(strings.TrimSpace(settings.GRPCSettings.ServiceName), "/")
		}
	}
	return "", ""
}

func xrayPublicPort(inboundPort int, hostFromListen bool) (int, error) {
	// A public listener already exposes its own port (notably Reality and
	// native TLS xHTTP). A global 443 override is only for loopback inbounds
	// fronted by nginx/CDN.
	if hostFromListen && inboundPort > 0 {
		return inboundPort, nil
	}
	if rawPort := strings.TrimSpace(os.Getenv(xrayPublicPortEnv)); rawPort != "" {
		port, err := strconv.Atoi(rawPort)
		if err != nil || port <= 0 || port > 65535 {
			return 0, fmt.Errorf("%s must be a TCP port", xrayPublicPortEnv)
		}
		return port, nil
	}
	return 443, nil
}

func xraySelectedClientUUID(clients []xrayClient, email string) (string, error) {
	client, err := xraySelectedClient(clients, email)
	if err != nil {
		return "", err
	}
	return client.ID, nil
}

func xrayClientByUUID(clients []xrayClient, uuid string) (xrayClient, bool) {
	wantedUUID := strings.TrimSpace(uuid)
	for _, client := range clients {
		if strings.TrimSpace(client.ID) == wantedUUID {
			client.ID = wantedUUID
			return client, true
		}
	}
	return xrayClient{}, false
}

// xrayExplicitClient validates an operator override whenever the local Xray
// config exposes its accepted clients. An empty list remains valid for staged
// credential rotation where the server-side list is intentionally unavailable.
func xrayExplicitClient(clients []xrayClient, uuid string) (xrayClient, error) {
	if len(clients) == 0 {
		return xrayClient{ID: uuid}, nil
	}
	client, ok := xrayClientByUUID(clients, uuid)
	if !ok {
		return xrayClient{}, fmt.Errorf("explicit client UUID is not accepted by the selected Xray inbound; update %s or use %s", xrayClientUUIDEnv, xrayClientEmailEnv)
	}
	return client, nil
}

func xraySelectedClient(clients []xrayClient, email string) (xrayClient, error) {
	preferredEmail := strings.TrimSpace(email)
	if preferredEmail != "" {
		for _, client := range clients {
			if strings.EqualFold(strings.TrimSpace(client.Email), preferredEmail) && strings.TrimSpace(client.ID) != "" {
				client.ID = strings.TrimSpace(client.ID)
				return client, nil
			}
		}
		return xrayClient{}, fmt.Errorf("client email %q not found; set %s or %s", preferredEmail, xrayClientEmailEnv, xrayClientUUIDEnv)
	}
	for _, client := range clients {
		if strings.TrimSpace(client.ID) != "" {
			client.ID = strings.TrimSpace(client.ID)
			return client, nil
		}
	}
	return xrayClient{}, fmt.Errorf("no VLESS client id in Xray inbound; set %s", xrayClientUUIDEnv)
}

func buildXrayVLESSURI(clientUUID, host string, port int, sni, fingerprint, transportHost, transportPath string) string {
	return buildXrayVLESSURIForTransport(clientUUID, host, port, sni, fingerprint, "httpupgrade", transportHost, transportPath)
}

func buildXrayVLESSURIForTransport(clientUUID, host string, port int, sni, fingerprint, transportType, transportHost, transportPath string) string {
	query := url.Values{}
	query.Set("type", transportType)
	if transportHost != "" {
		query.Set("host", transportHost)
	}
	if transportType == "grpc" {
		query.Set("serviceName", transportPath)
	} else {
		query.Set("path", transportPath)
	}
	query.Set("security", "tls")
	query.Set("sni", sni)
	query.Set("fp", fingerprint)
	query.Set("allowInsecure", "0")
	u := url.URL{
		Scheme:   "vless",
		User:     url.User(clientUUID),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: query.Encode(),
	}
	return u.String()
}

func optionalEnvInt(envName string) (int, error) {
	rawValue := strings.TrimSpace(os.Getenv(envName))
	if rawValue == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", envName)
	}
	return value, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func safeXrayTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "vless-httpupgrade"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, tag)
}
