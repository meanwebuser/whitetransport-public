package tunnel

import (
	"context"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestPhysicalBindingKeySharesMailboxRoleAliases(t *testing.T) {
	first, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{Token: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{Token: "second"})
	if err != nil {
		t.Fatal(err)
	}
	left := policy.CarrierBinding{Carrier: first, Endpoint: carriers.Endpoint{ID: "vk-messages", Address: "fixture-peer"}}
	right := policy.CarrierBinding{Carrier: second, Endpoint: carriers.Endpoint{ID: "vk-egress", Address: "fixture-peer"}}
	if got, want := physicalBindingKey(left), physicalBindingKey(right); got != want {
		t.Fatalf("mailbox role aliases must share physical reader key, got %q and %q", got, want)
	}
}

func TestPhysicalBindingKeyKeepsStreamInstancesSeparate(t *testing.T) {
	left := policy.CarrierBinding{Carrier: &testPhysicalStreamCarrier{id: "left"}, Endpoint: carriers.Endpoint{Address: "room"}}
	right := policy.CarrierBinding{Carrier: &testPhysicalStreamCarrier{id: "right"}, Endpoint: carriers.Endpoint{Address: "room"}}
	if physicalBindingKey(left) == physicalBindingKey(right) {
		t.Fatal("independent stream instances must keep separate reader keys")
	}
}

type testPhysicalStreamCarrier struct {
	carriers.BaseCarrier
	id string
}

func (c *testPhysicalStreamCarrier) Descriptor() carriers.Descriptor {
	return carriers.Descriptor{ID: c.id, Provider: "test", Mode: carriers.DeliveryStream}
}
func (*testPhysicalStreamCarrier) IsNative() {}
func (*testPhysicalStreamCarrier) Write(context.Context, carriers.Endpoint, fabric.Envelope) error {
	return nil
}
func (*testPhysicalStreamCarrier) Read(context.Context, carriers.Endpoint, carriers.Cursor) (carriers.ReadResult, error) {
	return carriers.ReadResult{}, nil
}
func (*testPhysicalStreamCarrier) Probe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{}, nil
}
