package chatdiscovery

import (
	"testing"
)

func TestAllocateChats(t *testing.T) {
	chats := []DiscoveredChat{
		{Platform: "vk", PeerID: "100", Type: "user"},
		{Platform: "vk", PeerID: "200", Type: "user"},
		{Platform: "vk", PeerID: "300", Type: "user"},
		{Platform: "vk", PeerID: "400", Type: "user"},
		{Platform: "vk", PeerID: "500", Type: "user"},
		{Platform: "vk", PeerID: "600", Type: "user"},
	}
	alloc := AllocateChats(chats)
	if len(alloc.Discovery) != 1 {
		t.Errorf("discovery: expected 1, got %d", len(alloc.Discovery))
	}
	if alloc.Discovery[0] != "100" {
		t.Errorf("discovery: expected 100, got %s", alloc.Discovery[0])
	}
	if len(alloc.Control) != 1 || alloc.Control[0] != "200" {
		t.Errorf("control: expected [200], got %v", alloc.Control)
	}
	if len(alloc.Logs) != 1 || alloc.Logs[0] != "300" {
		t.Errorf("logs: expected [300], got %v", alloc.Logs)
	}
	if len(alloc.Admin) != 1 || alloc.Admin[0] != "400" {
		t.Errorf("admin: expected [400], got %v", alloc.Admin)
	}
	if len(alloc.Egress) != 2 {
		t.Errorf("egress: expected 2, got %d", len(alloc.Egress))
	}
}

func TestAllocateChatsSingle(t *testing.T) {
	chats := []DiscoveredChat{
		{PeerID: "100"},
	}
	alloc := AllocateChats(chats)
	if len(alloc.Discovery) != 1 {
		t.Errorf("discovery: expected 1, got %d", len(alloc.Discovery))
	}
	if len(alloc.Egress) != 1 {
		t.Errorf("egress: expected 1 (fallback), got %d", len(alloc.Egress))
	}
}

func TestAllocateChatsEmpty(t *testing.T) {
	alloc := AllocateChats(nil)
	if alloc.Discovery != nil || alloc.Egress != nil {
		t.Error("empty allocation should be zero")
	}
}
