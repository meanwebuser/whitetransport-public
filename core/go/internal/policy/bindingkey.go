package policy

import "strings"

// MakeBindingKey builds a compound binding key from a carrier ID and role.
// When role is empty the plain carrier ID is returned (legacy single-binding).
//
// Examples:
//
//	MakeBindingKey("vk.messages", "discovery")   → "vk.messages:discovery"
//	MakeBindingKey("vk.messages", "")             → "vk.messages"
func MakeBindingKey(carrierID, role string) string {
	if role == "" {
		return carrierID
	}
	return carrierID + ":" + role
}

// ParseBindingKey splits a compound binding key into carrier ID and role.
// Plain keys (no colon separator) return role = "".
//
// Examples:
//
//	ParseBindingKey("vk.messages:discovery") → ("vk.messages", "discovery")
//	ParseBindingKey("vk.messages")           → ("vk.messages", "")
func ParseBindingKey(key string) (carrierID, role string) {
	// Find the LAST colon so carrier IDs containing dots are handled correctly.
	// Carrier IDs use dots (vk.messages) not colons, so the first colon is the
	// role separator. We use IndexByte for O(n) single-pass.
	idx := strings.IndexByte(key, ':')
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}

// CarrierIDFromBindingKey extracts the carrier ID portion of a binding key.
// Equivalent to the first return value of ParseBindingKey.
func CarrierIDFromBindingKey(key string) string {
	carrierID, _ := ParseBindingKey(key)
	return carrierID
}

// HasBindingKeyPrefix reports whether the given binding key belongs to the
// specified carrier ID. It matches both exact keys ("vk.messages") and
// compound keys ("vk.messages:discovery").
func HasBindingKeyPrefix(bindingKey, carrierID string) bool {
	if bindingKey == carrierID {
		return true
	}
	return strings.HasPrefix(bindingKey, carrierID+":")
}
