package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestHandleAnswerEnforcesProductCompatibilityBeforeDelivery(t *testing.T) {
	tests := []struct {
		name          string
		remoteVersion string
		wantAnswer    bool
	}{
		{name: "same line mixed patch", remoteVersion: "0.1.123", wantAnswer: true},
		{name: "cross line", remoteVersion: "0.2.1"},
		{name: "missing"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answerCh := make(chan session.Answer, 1)
			errorCh := make(chan session.SessionError, 1)
			control := &ControlPlane{
				productVersion:     "0.1.1",
				pending:            map[string]chan session.Answer{"session-1": answerCh},
				pendingErrors:      map[string]chan session.SessionError{"session-1": errorCh},
				pendingTargetNodes: map[string]string{"session-1": "node-1"},
				state:              statusStateConnecting,
			}

			control.handleAnswer(session.Answer{
				SessionID:      "session-1",
				NodeID:         "node-1",
				ProductVersion: tc.remoteVersion,
			})

			if tc.wantAnswer {
				select {
				case <-answerCh:
				default:
					t.Fatal("compatible answer was not delivered")
				}
				select {
				case got := <-errorCh:
					t.Fatalf("compatible answer produced error: %+v", got)
				default:
				}
				return
			}

			select {
			case got := <-errorCh:
				if got.Code != incompatibleProductVersionCode {
					t.Fatalf("error code = %q, want %q", got.Code, incompatibleProductVersionCode)
				}
			case <-time.After(time.Second):
				t.Fatal("incompatible answer did not fail pending connect")
			}
			select {
			case got := <-answerCh:
				t.Fatalf("incompatible answer was delivered: %+v", got)
			default:
			}
			if control.Status().State != statusStateDisconnected {
				t.Fatalf("state = %q, want disconnected", control.Status().State)
			}
		})
	}
}

func TestHandleOfferRejectsIncompatibleProductBeforeNodeMutation(t *testing.T) {
	for _, remoteVersion := range []string{"0.2.1", ""} {
		name := remoteVersion
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://product-version-" + name}
			carrier := newMemoryCarrierWithDescriptor(t, "product-version-"+name, carriers.CarrierVKMessages)
			binding := policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}
			packetTunnel := &packetLifecycleTestTunnel{}
			sshIssuer := &fakeSessionSSHIssuer{}
			control, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "node-1", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
				carriers.CarrierVKMessages: binding,
			}, policy.DefaultAdaptivePolicy(), packetTunnel)
			if err != nil {
				t.Fatal(err)
			}
			control.productVersion = "0.1.123"
			control.advertised = true
			control.sessionSSHIssuer = sshIssuer

			control.handleOffer(context.Background(), session.Offer{
				SessionID:      "session-1",
				ClientID:       "client-1",
				TargetNodeID:   "node-1",
				ProductVersion: remoteVersion,
				ReplyEndpoints: []carriers.Endpoint{endpoint},
				ExpiresAt:      time.Now().Add(time.Minute),
			}, carrierRef{ID: carriers.CarrierVKMessages, Descriptor: carrier.Descriptor(), Binding: binding})

			if control.nodeBusy || control.nodeSessionID != "" || control.nodeSessionClientID != "" {
				t.Fatalf("incompatible offer mutated node session: busy=%v session=%q client=%q", control.nodeBusy, control.nodeSessionID, control.nodeSessionClientID)
			}
			if !control.advertised {
				t.Fatal("incompatible offer withdrew node advertisement")
			}
			sshIssuer.mu.Lock()
			issueCount := sshIssuer.issueCount
			sshIssuer.mu.Unlock()
			if issueCount != 0 {
				t.Fatalf("incompatible offer issued %d SSH leases", issueCount)
			}
			packetTunnel.mu.Lock()
			packetEvents := append([]string(nil), packetTunnel.events...)
			packetTunnel.mu.Unlock()
			if len(packetEvents) != 0 {
				t.Fatalf("incompatible offer activated packet session: %+v", packetEvents)
			}
			read, err := carrier.Read(context.Background(), endpoint, "")
			if err != nil {
				t.Fatal(err)
			}
			var errorsSeen, acksSeen, answersSeen int
			for _, envelope := range read.Envelopes {
				switch envelope.PayloadType {
				case session.PayloadSessionError:
					errorsSeen++
					got, decodeErr := session.DecodePayload[session.SessionError](envelope.Payload)
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					if got.Code != incompatibleProductVersionCode {
						t.Fatalf("error code = %q, want %q", got.Code, incompatibleProductVersionCode)
					}
				case session.PayloadSessionOfferAck:
					acksSeen++
				case session.PayloadSessionAnswer:
					answersSeen++
				}
			}
			if errorsSeen != 1 || acksSeen != 0 || answersSeen != 0 {
				t.Fatalf("payload counts: errors=%d acks=%d answers=%d", errorsSeen, acksSeen, answersSeen)
			}
		})
	}
}
