package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func newControlAuthBoundaryNode(t *testing.T, bootstrapSecret string) (*ControlPlane, *memoryCarrier, carriers.Endpoint) {
	t.Helper()
	endpoint := carriers.Endpoint{ID: "auth-boundary-control", Carrier: "auth-boundary-control", Address: "control"}
	carrier := newMemoryCarrierWithCustomDescriptor(endpoint.Carrier, fabric.TrafficBootstrap, fabric.TrafficControl)
	control, err := newTestControlPlane(config.Config{
		Role:            config.RoleNode,
		NodeID:          "auth-boundary-node",
		BootstrapSecret: bootstrapSecret,
	}, map[string]policy.CarrierBinding{
		endpoint.ID: {Carrier: carrier, Endpoint: endpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	return control, carrier, endpoint
}

func keylessOfferEnvelope(t *testing.T, version int) fabric.Envelope {
	t.Helper()
	payload, err := session.EncodePayload(session.Offer{
		SessionID: "auth-boundary-session",
		ClientID:  "auth-boundary-client",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}
	envelope := fabric.NewEnvelope("auth-boundary-envelope", fabric.TrafficControl, session.PayloadSessionOffer, payload)
	envelope.Version = version
	return envelope
}

func assertNoOfferAck(t *testing.T, carrier *memoryCarrier, endpoint carriers.Endpoint) {
	t.Helper()
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read control mailbox: %v", err)
	}
	for _, envelope := range read.Envelopes {
		if envelope.PayloadType == session.PayloadSessionOfferAck {
			t.Fatalf("invalid offer reached dispatch and produced an ACK: %+v", envelope)
		}
	}
}

func TestControlPlaneDirectPollValidatesEnvelopeBeforeDispatch(t *testing.T) {
	control, carrier, endpoint := newControlAuthBoundaryNode(t, "")
	if err := carrier.Write(context.Background(), endpoint, keylessOfferEnvelope(t, fabric.CurrentVersion+1)); err != nil {
		t.Fatalf("write malformed envelope: %v", err)
	}

	control.pollOnce(context.Background())

	if control.nodeBusy {
		t.Fatal("direct poll dispatched an invalid envelope and marked node busy")
	}
	if control.nodeSessionID != "" {
		t.Fatalf("direct poll created node session %q from invalid envelope", control.nodeSessionID)
	}
	assertNoOfferAck(t, carrier, endpoint)
}

func TestControlPlaneRouterValidatesEnvelopeBeforeDispatch(t *testing.T) {
	control, carrier, endpoint := newControlAuthBoundaryNode(t, "")
	if len(control.control) != 1 {
		t.Fatalf("control refs = %d, want one", len(control.control))
	}

	control.handleControlEnvelope(context.Background(), keylessOfferEnvelope(t, fabric.CurrentVersion+1), control.control[0])

	if control.nodeBusy {
		t.Fatal("router dispatched an invalid envelope and marked node busy")
	}
	if control.nodeSessionID != "" {
		t.Fatalf("router created node session %q from invalid envelope", control.nodeSessionID)
	}
	assertNoOfferAck(t, carrier, endpoint)
}

func TestControlPlaneAuthenticatedNodeRejectsKeylessOfferBeforeMutation(t *testing.T) {
	control, carrier, endpoint := newControlAuthBoundaryNode(t, "shared-bootstrap-secret")
	if control.bootstrapCipher == nil {
		t.Fatal("test node did not configure an authenticated bootstrap cipher")
	}

	control.handleControlEnvelope(context.Background(), keylessOfferEnvelope(t, fabric.CurrentVersion), carrierRef{
		ID:         endpoint.ID,
		Descriptor: control.control[0].Descriptor,
		Binding:    control.control[0].Binding,
	})

	if control.nodeBusy {
		t.Fatal("authenticated node accepted a keyless offer and became busy")
	}
	if control.nodeSessionID != "" {
		t.Fatalf("authenticated node created session %q for keyless offer", control.nodeSessionID)
	}
	if control.nodeSessionClientID != "" {
		t.Fatalf("authenticated node recorded client %q for keyless offer", control.nodeSessionClientID)
	}
	assertNoOfferAck(t, carrier, endpoint)
}

func TestControlPlaneLegacyNodeKeepsKeylessOfferCompatibility(t *testing.T) {
	control, _, _ := newControlAuthBoundaryNode(t, "")

	if _, encrypted, err := control.offerSessionKey(session.Offer{}); err != nil || encrypted {
		t.Fatalf("legacy keyless offer rejected: encrypted=%v err=%v", encrypted, err)
	}
}

func TestControlPlaneAuthenticatedNodeAcceptsValidEncryptedSessionKey(t *testing.T) {
	control, _, _ := newControlAuthBoundaryNode(t, "shared-bootstrap-secret")
	want, err := fabric.GenerateSessionKey()
	if err != nil {
		t.Fatalf("generate session key: %v", err)
	}
	encrypted, err := session.EncryptSessionKey(control.bootstrapCipher, want[:])
	if err != nil {
		t.Fatalf("encrypt session key: %v", err)
	}

	got, encryptedDelivery, err := control.offerSessionKey(session.Offer{SessionKey: encrypted})
	if err != nil {
		t.Fatalf("valid encrypted offer rejected: %v", err)
	}
	if !encryptedDelivery {
		t.Fatal("valid encrypted offer was treated as legacy delivery")
	}
	if got != want {
		t.Fatalf("decrypted session key mismatch: got %x, want %x", got, want)
	}
}
