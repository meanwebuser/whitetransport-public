package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestProtocolGoldenJSON(t *testing.T) {
	expiresAt := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	heartbeatAt := time.Date(2026, 6, 8, 12, 5, 0, 0, time.UTC)

	testCases := []struct {
		name   string
		value  any
		golden string
	}{
		{
			name: "node_advertisement",
			value: NodeAdvertisement{
				NodeID:       "example-exit-node",
				Role:         RoleNode,
				Label:        "Example Exit Node",
				Country:      "NL",
				Region:       "AMS",
				Capabilities: []string{"egress", "bulk"},
				Carriers: []carriers.Endpoint{
					{ID: "vk-control", Carrier: "vk.messages", Address: "peer:PLACEHOLDER_VK_PEER_ID", Metadata: map[string]string{"scope": "control"}},
				},
			},
			golden: "node_advertisement.json",
		},
		{
			name:   "node_withdrawal",
			value:  NodeWithdrawal{NodeID: "example-exit-node"},
			golden: "node_withdrawal.json",
		},
		{
			name: "offer",
			value: Offer{
				SessionID: "sess-123",
				ClientID:  "client-1",
				Wanted:    []string{"wbstream.vp8", "vk.messages"},
				UsableCarriers: []carriers.Descriptor{
					{
						ID:             "vk.messages",
						Provider:       "vk",
						Mode:           carriers.DeliveryMailbox,
						TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl, fabric.TrafficEgress},
						Capabilities:   []carriers.Capability{carriers.CapDuplex},
					},
				},
				ReplyEndpoints: []carriers.Endpoint{
					{ID: "ok-reply", Carrier: "ok.messages", Address: "chat:C5cbdd516a400", Metadata: map[string]string{"scope": "reply"}},
				},
				ExpiresAt:  expiresAt,
				SessionKey: []byte("x"),
				Metadata:   map[string]string{"platform": "desktop"},
			},
			golden: "offer.json",
		},
		{
			name: "answer",
			value: Answer{
				SessionID: "sess-123",
				NodeID:    "example-exit-node",
				Label:     "Example Exit Node",
				Country:   "NL",
				Region:    "AMS",
				Endpoints: []carriers.Endpoint{
					{ID: "vk-control", Carrier: "vk.messages", Address: "peer:PLACEHOLDER_VK_PEER_ID", Metadata: map[string]string{"scope": "control"}},
				},
				EgressEndpoints: []carriers.Endpoint{
					{ID: "wb-egress", Carrier: "wbstream.vp8", Address: "wbstream://room-123", Metadata: map[string]string{"mode": "egress"}},
				},
				ExpiresAt: expiresAt,
			},
			golden: "answer.json",
		},
		{
			name: "offer_ack",
			value: OfferAck{
				SessionID:  "sess-123",
				Status:     "busy",
				RetryAfter: 30 * time.Second,
				Error:      "node busy",
			},
			golden: "offer_ack.json",
		},
		{
			name: "node_heartbeat",
			value: NodeHeartbeat{
				NodeID:    "example-exit-node",
				Timestamp: heartbeatAt,
			},
			golden: "node_heartbeat.json",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.value, "", "  ")
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden %s: %v", tc.golden, err)
			}
			if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
				t.Fatalf("golden mismatch for %s\nwant:\n%s\ngot:\n%s", tc.name, string(want), string(got))
			}
		})
	}
}
