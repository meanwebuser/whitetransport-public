package carriers

import (
	"bytes"
	"context"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestEncryptedProviderCarrierSealsProviderWireData(t *testing.T) {
	transport := &providerWireTestProvider{id: "vkcall"}
	endpoint := Endpoint{ID: "vkcall-session", Carrier: "vkcall", Address: "room://test"}
	key := [32]byte{1, 2, 3}
	carrier, err := NewEncryptedProviderCarrier(transport, endpoint, key)
	if err != nil {
		t.Fatalf("NewEncryptedProviderCarrier: %v", err)
	}

	envelope := fabric.NewEnvelope("provider-wire-1", fabric.TrafficEgress, "tunnel.data", []byte("provider-wire-secret"))
	if err := carrier.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(transport.messages) != 1 {
		t.Fatalf("provider messages = %d, want 1", len(transport.messages))
	}
	if bytes.Contains(transport.messages[0], envelope.Payload) {
		t.Fatalf("provider observed plaintext payload: %q", transport.messages[0])
	}

	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Envelopes) != 1 || !bytes.Equal(read.Envelopes[0].Payload, envelope.Payload) {
		t.Fatalf("opened envelopes = %#v, want payload %q", read.Envelopes, envelope.Payload)
	}
}

func TestEncryptedProviderCarrierRejectsTamperingAndWrongCarrierScope(t *testing.T) {
	endpoint := Endpoint{ID: "vkcall-session", Carrier: "vkcall", Address: "room://test"}
	key := [32]byte{4, 5, 6}
	writerTransport := &providerWireTestProvider{id: "vkcall"}
	writer, err := NewEncryptedProviderCarrier(writerTransport, endpoint, key)
	if err != nil {
		t.Fatalf("NewEncryptedProviderCarrier writer: %v", err)
	}

	envelope := fabric.NewEnvelope("provider-wire-2", fabric.TrafficEgress, "tunnel.data", []byte("authenticated-data"))
	if err := writer.Write(context.Background(), endpoint, envelope); err != nil {
		t.Fatalf("Write: %v", err)
	}

	tampered := append([]byte(nil), writerTransport.messages[0]...)
	tampered[len(tampered)-1] ^= 0x01
	tamperedTransport := &providerWireTestProvider{id: "vkcall", messages: [][]byte{tampered}}
	tamperedReader, err := NewEncryptedProviderCarrier(tamperedTransport, endpoint, key)
	if err != nil {
		t.Fatalf("NewEncryptedProviderCarrier tampered reader: %v", err)
	}
	if _, err := tamperedReader.Read(context.Background(), endpoint, ""); err == nil {
		t.Fatal("Read accepted tampered provider wire data")
	}

	wrongScopeTransport := &providerWireTestProvider{id: "wbstream", messages: [][]byte{writerTransport.messages[0]}}
	wrongScopeReader, err := NewEncryptedProviderCarrier(wrongScopeTransport, Endpoint{ID: "wbstream-session", Carrier: "wbstream", Address: "room://test"}, key)
	if err != nil {
		t.Fatalf("NewEncryptedProviderCarrier wrong scope reader: %v", err)
	}
	if _, err := wrongScopeReader.Read(context.Background(), endpoint, ""); err == nil {
		t.Fatal("Read accepted ciphertext from a different provider carrier scope")
	}
}

type providerWireTestProvider struct {
	id       string
	messages [][]byte
}

func (p *providerWireTestProvider) ID() string                              { return p.id }
func (p *providerWireTestProvider) Type() provider.Type                     { return provider.TypeVideoCall }
func (p *providerWireTestProvider) Category() provider.Category             { return provider.CategoryVideo }
func (p *providerWireTestProvider) Version() string                         { return "test" }
func (p *providerWireTestProvider) Configure(provider.ProviderConfig) error { return nil }
func (p *providerWireTestProvider) GetSchema() provider.Schema              { return provider.Schema{} }
func (p *providerWireTestProvider) Health() provider.Health                 { return provider.Health{} }
func (p *providerWireTestProvider) GetLimits() provider.Limits {
	return provider.Limits{MaxPayloadBytes: 4096}
}
func (p *providerWireTestProvider) GetMetrics() provider.Metrics   { return provider.Metrics{} }
func (p *providerWireTestProvider) UpdateMetrics(provider.Metrics) {}
func (p *providerWireTestProvider) Load() error                    { return nil }
func (p *providerWireTestProvider) Unload() error                  { return nil }
func (p *providerWireTestProvider) Send(_ context.Context, data []byte) error {
	p.messages = append(p.messages, append([]byte(nil), data...))
	return nil
}
func (p *providerWireTestProvider) Receive(_ context.Context) ([]byte, error) {
	if len(p.messages) == 0 {
		return nil, nil
	}
	data := p.messages[0]
	p.messages = p.messages[1:]
	return data, nil
}
