package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

func TestProviderCarrier_WriteReadSingle(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("file.mailbox", func() provider.Provider {
		return &mockProvider{name: "file.mailbox"}
	})

	adapter, err := reg.Create("file.mailbox", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("create file.mailbox: %v", err)
	}

	endpoint := carriers.Endpoint{
		ID:      "test",
		Carrier: "file.mailbox",
		Address: "test-address",
	}

	wrapped, err := carriers.NewProviderCarrier(adapter, endpoint)
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}

	env := fabric.NewEnvelope("test-1", fabric.TrafficControl, "test", []byte("hello"))
	ctx := context.Background()

	if err := wrapped.Write(ctx, endpoint, env); err != nil {
		t.Fatalf("Write: %v", err)
	}

	result, err := wrapped.Read(ctx, endpoint, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(result.Envelopes) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(result.Envelopes))
	}
	if result.Envelopes[0].ID != "test-1" {
		t.Fatalf("expected envelope ID test-1, got %s", result.Envelopes[0].ID)
	}
	if string(result.Envelopes[0].Payload) != "hello" {
		t.Fatalf("expected payload 'hello', got %q", string(result.Envelopes[0].Payload))
	}

	desc := wrapped.Descriptor()
	if desc.ID != "file.mailbox" {
		t.Fatalf("expected descriptor ID file.mailbox, got %s", desc.ID)
	}
}

func TestProviderCarrier_Probe(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("test", func() provider.Provider {
		return &mockProvider{name: "test"}
	})

	adapter, err := reg.Create("test", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wrapped, err := carriers.NewProviderCarrier(adapter, carriers.Endpoint{
		ID: "test", Carrier: "test",
	})
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}

	metrics, err := wrapped.Probe(context.Background(), carriers.Endpoint{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !metrics.Healthy {
		t.Fatal("expected healthy probe")
	}
}

func TestProviderCarrier_WriteMultipleSequential(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("multi", func() provider.Provider {
		return &mockProvider{name: "multi"}
	})

	adapter, err := reg.Create("multi", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wrapped, err := carriers.NewProviderCarrier(adapter, carriers.Endpoint{
		ID: "multi", Carrier: "multi",
	})
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}

	ctx := context.Background()
	ep := carriers.Endpoint{}

	count := 5
	for i := range count {
		env := fabric.NewEnvelope(
			fmt.Sprintf("multi-%d", i),
			fabric.TrafficControl,
			"test",
			[]byte(fmt.Sprintf("payload-%d", i)),
		)
		if err := wrapped.Write(ctx, ep, env); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	for i := range count {
		result, err := wrapped.Read(ctx, ep, "")
		if err != nil {
			t.Fatalf("Read %d: %v", i, err)
		}
		if len(result.Envelopes) != 1 {
			t.Fatalf("Read %d: expected 1 envelope, got %d", i, len(result.Envelopes))
		}
		if string(result.Envelopes[0].Payload) != fmt.Sprintf("payload-%d", i) {
			t.Fatalf("Read %d: expected payload-%d, got %q", i, i, string(result.Envelopes[0].Payload))
		}
	}
}

func TestProviderCarrier_ReadFromEmpty(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	reg := NewProviderRegistry(ps, ks)

	reg.Register("empty", func() provider.Provider {
		return &mockProvider{name: "empty"}
	})

	adapter, err := reg.Create("empty", provider.ProviderConfig{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wrapped, err := carriers.NewProviderCarrier(adapter, carriers.Endpoint{
		ID: "empty", Carrier: "empty",
	})
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}

	result, err := wrapped.Read(context.Background(), carriers.Endpoint{}, "")
	if err != nil {
		t.Fatalf("Read from empty: %v", err)
	}
	if len(result.Envelopes) != 0 {
		t.Fatalf("expected 0 envelopes from empty carrier, got %d", len(result.Envelopes))
	}
}

func TestProviderCarrier_DescriptorFromAdapter(t *testing.T) {
	mock := &mockProvider{name: "test-carrier"}

	endpoint := carriers.Endpoint{ID: "test", Carrier: "test-carrier"}
	wrapped, err := carriers.NewProviderCarrier(mock, endpoint)
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}

	desc := wrapped.Descriptor()
	if desc.ID != "test-carrier" {
		t.Fatalf("expected ID test-carrier, got %s", desc.ID)
	}
	if desc.Provider != "test-carrier" {
		t.Fatalf("expected Provider test-carrier, got %s", desc.Provider)
	}
	if desc.Mode != carriers.DeliveryMailbox {
		t.Fatalf("expected mode mailbox, got %s", desc.Mode)
	}
}
