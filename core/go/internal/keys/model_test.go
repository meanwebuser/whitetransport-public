package keys

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestStore_AddAndGet(t *testing.T) {
	s := NewStore()

	k := &Model{
		ID:         "vk-token-1",
		ProviderID: "vk",
		Type:       provider.KeyPermanent,
		Token:      "secret-token",
		Status:     provider.KeyActive,
		CreatedAt:  time.Now(),
	}

	s.Set(k)
	got, ok := s.Get("vk-token-1")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if got.Token != "secret-token" {
		t.Fatalf("expected secret-token, got %s", got.Token)
	}
}

func TestStore_GetMissing(t *testing.T) {
	s := NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected missing key")
	}
}

func TestStore_List(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", ProviderID: "vk"})
	s.Set(&Model{ID: "b", ProviderID: "ok"})
	s.Set(&Model{ID: "c", ProviderID: "vk"})

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(list))
	}
}

func TestStore_ListByProvider(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", ProviderID: "vk"})
	s.Set(&Model{ID: "b", ProviderID: "ok"})
	s.Set(&Model{ID: "c", ProviderID: "vk"})

	vkKeys := s.ListByProvider("vk")
	if len(vkKeys) != 2 {
		t.Fatalf("expected 2 vk keys, got %d", len(vkKeys))
	}
}

func TestStore_ListByType(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", Type: provider.KeyPermanent})
	s.Set(&Model{ID: "b", Type: provider.KeyTemporary})
	s.Set(&Model{ID: "c", Type: provider.KeyPermanent})

	perm := s.ListByType(provider.KeyPermanent)
	if len(perm) != 2 {
		t.Fatalf("expected 2 permanent keys, got %d", len(perm))
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "test"})
	s.Delete("test")
	_, ok := s.Get("test")
	if ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestStore_Concurrent(t *testing.T) {
	s := NewStore()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Set(&Model{ID: "a"})
			s.Get("a")
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			s.List()
			s.ListByProvider("vk")
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			s.Set(&Model{ID: "b"})
			s.Delete("b")
		}
		done <- struct{}{}
	}()

	for i := 0; i < 3; i++ {
		<-done
	}
}

func TestKeyWithChannels(t *testing.T) {
	k := &Model{
		ID: "multi-channel",
		Channels: map[string]ChannelConfig{
			"chat": {Enabled: true, MaxRate: 120, MaxSize: 4096},
			"docs": {Enabled: true, MaxRate: 180, MaxSize: 192 * 1024},
		},
		Health: Health{
			SuccessRate: 0.95,
			AvgLatency:  time.Second,
		},
	}

	if len(k.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(k.Channels))
	}
	if k.Health.SuccessRate != 0.95 {
		t.Fatalf("expected 0.95 success rate, got %f", k.Health.SuccessRate)
	}
}
