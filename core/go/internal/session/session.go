package session

import (
	"encoding/json"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// Role identifies whether a participant is acting as a client or a node.
type Role string

const (
	RoleClient Role = "client"
	RoleNode   Role = "node"
)

// NodeAdvertisement announces a node's control-plane identity and the carrier
// endpoints clients can use to reach it.  The node advertises once when it
// starts and withdraws (NodeWithdrawal) as soon as a session is accepted.
// EgressEndpoints are intentionally omitted — the room/address is created
// fresh per-session and returned in the Answer.
type NodeAdvertisement struct {
	NodeID       string              `json:"node_id"`
	Role         Role                `json:"role"`
	Label        string              `json:"label,omitempty"`
	Country      string              `json:"country,omitempty"`
	Region       string              `json:"region,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
	Carriers     []carriers.Endpoint `json:"carriers"`
}

// NodeWithdrawal tells clients that a node is no longer accepting new sessions.
// It is published on the same bootstrap carrier immediately after the node
// accepts a session.offer, so other clients stop trying.
type NodeWithdrawal struct {
	NodeID string `json:"node_id"`
}

// Offer requests a session and advertises the client's control-plane reply
// endpoints plus the carrier descriptors it can use.
type Offer struct {
	SessionID      string                `json:"session_id"`
	ClientID       string                `json:"client_id"`
	TargetNodeID   string                `json:"target_node_id,omitempty"` // If set, only this node should respond
	Wanted         []string              `json:"wanted,omitempty"`
	UsableCarriers []carriers.Descriptor `json:"usable_carriers,omitempty"`
	ReplyEndpoints []carriers.Endpoint   `json:"reply_endpoints,omitempty"`
	ExpiresAt      time.Time             `json:"expires_at,omitempty"`
	SessionKey     []byte                `json:"session_key,omitempty"` // AES-256-GCM encrypted session key
	Metadata       map[string]string     `json:"metadata,omitempty"`
	// ClientRoomEndpoint is set when the client created the egress room locally
	// using its own platform credentials. The node should join this room as a
	// guest instead of creating its own room. Format: "<carrier>://<address>"
	// e.g. "wbstream://room-xyz". If empty, the node creates the room (legacy).
	ClientRoomEndpoint string `json:"client_room_endpoint,omitempty"`
}

// Answer confirms the control-plane session and returns the egress carrier
// endpoints selected by the node.
type Answer struct {
	SessionID       string              `json:"session_id"`
	NodeID          string              `json:"node_id"`
	Label           string              `json:"label,omitempty"`
	Country         string              `json:"country,omitempty"`
	Region          string              `json:"region,omitempty"`
	Endpoints       []carriers.Endpoint `json:"endpoints,omitempty"`
	EgressEndpoints []carriers.Endpoint `json:"egress_endpoints,omitempty"`
	// EgressProfilesCiphertext holds session-key-encrypted runtime profiles for
	// egress endpoints. It is intentionally opaque to carriers and status APIs.
	EgressProfilesCiphertext []byte `json:"egress_profiles_ciphertext,omitempty"`
	ExpiresAt       time.Time           `json:"expires_at"`
	// JoinedClientRoom is true when the node joined the client's pre-created
	// room (ClientRoomEndpoint in the offer). In that case EgressEndpoints is
	// empty because the room was created on the client side.
	JoinedClientRoom bool `json:"joined_client_room,omitempty"`
}

// OfferAck is sent by the node immediately upon receiving a session offer,
// before processing it. Status is one of "received", "busy", or "error".
// When busy, RetryAfter suggests how long the client should wait before retrying.
type OfferAck struct {
	SessionID  string        `json:"session_id"`
	Status     string        `json:"status"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// Release tells a node that the client has intentionally ended a session.
// It is a control-plane message, separate from data-tunnel close events, so
// normal GUI disconnects do not leave the node busy until session expiry.
type Release struct {
	SessionID string `json:"session_id"`
	ClientID  string `json:"client_id"`
	NodeID    string `json:"node_id"`
	Reason    string `json:"reason,omitempty"`
}

// NodeHeartbeat is published periodically by active nodes so clients can
// detect stale advertisements.
type NodeHeartbeat struct {
	NodeID    string    `json:"node_id"`
	Timestamp time.Time `json:"timestamp"`
}

// SessionError is sent by either party when a session operation fails.
// It provides explicit error signaling instead of relying on timeouts.
type SessionError struct {
	SessionID string `json:"session_id"`
	SenderID  string `json:"sender_id"` // client_id or node_id
	Error     string `json:"error"`
	Code      string `json:"code,omitempty"` // error code: "timeout", "carrier_failed", "invalid_offer", etc.
}

// EncodePayload serializes a session payload into JSON.
func EncodePayload(value any) ([]byte, error) {
	return json.Marshal(value)
}

// DecodePayload deserializes a session payload from JSON.
func DecodePayload[T any](payload []byte) (T, error) {
	var value T
	err := json.Unmarshal(payload, &value)
	return value, err
}
