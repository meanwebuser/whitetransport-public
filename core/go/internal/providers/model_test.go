package providers

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestStore_AddAndGet(t *testing.T) {
	s := NewStore()
	p := &Model{
		ID:       "vk",
		Name:     "VKontakte",
		Type:     provider.TypeMessaging,
		Category: provider.CategorySocial,
		Version:  "1.0.0",
	}

	s.Set(p)
	got, ok := s.Get("vk")
	if !ok {
		t.Fatal("expected provider to exist")
	}
	if got.Name != "VKontakte" {
		t.Fatalf("expected VKontakte, got %s", got.Name)
	}
}

func TestStore_GetMissing(t *testing.T) {
	s := NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected nonexistent provider to be missing")
	}
}

func TestStore_List(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", Type: provider.TypeMessaging, Category: provider.CategorySocial})
	s.Set(&Model{ID: "b", Type: provider.TypeVideoCall, Category: provider.CategoryVideo})
	s.Set(&Model{ID: "c", Type: provider.TypeMessaging, Category: provider.CategorySocial})

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(list))
	}
}

func TestStore_ListByType(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", Type: provider.TypeMessaging})
	s.Set(&Model{ID: "b", Type: provider.TypeVideoCall})
	s.Set(&Model{ID: "c", Type: provider.TypeMessaging})

	msgs := s.ListByType(provider.TypeMessaging)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messaging providers, got %d", len(msgs))
	}
}

func TestStore_ListByCategory(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "a", Category: provider.CategorySocial})
	s.Set(&Model{ID: "b", Category: provider.CategoryVideo})
	s.Set(&Model{ID: "c", Category: provider.CategorySocial})

	social := s.ListByCategory(provider.CategorySocial)
	if len(social) != 2 {
		t.Fatalf("expected 2 social providers, got %d", len(social))
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewStore()
	s.Set(&Model{ID: "test", Name: "Test"})
	s.Delete("test")
	_, ok := s.Get("test")
	if ok {
		t.Fatal("expected provider to be deleted")
	}
}

func TestModelWithEncoding(t *testing.T) {
	p := &Model{
		ID: "provider-with-encoding",
		Encoding: EncodingConfig{
			Charset:            "UTF-8",
			MaxPayload:         4096,
			SupportsCompression: true,
		},
	}
	s := NewStore()
	s.Set(p)

	got, _ := s.Get("provider-with-encoding")
	if got.Encoding.Charset != "UTF-8" {
		t.Fatalf("expected UTF-8, got %s", got.Encoding.Charset)
	}
	if !got.Encoding.SupportsCompression {
		t.Fatal("expected compression support")
	}
}

func TestModelWithRateLimits(t *testing.T) {
	expiry := time.Now().Add(24 * time.Hour)
	p := &Model{
		ID: "limited",
		Keys: []KeyRef{
			{
				ID:        "key-1",
				Type:      provider.KeyPermanent,
				Status:    provider.KeyActive,
				ExpiresAt: &expiry,
			},
		},
		RateLimits: RateLimitsConfig{
			MessagesPerMinute: 120,
			BytesPerDay:       1_000_000_000,
		},
	}
	s := NewStore()
	s.Set(p)

	got, _ := s.Get("limited")
	if len(got.Keys) != 1 {
		t.Fatalf("expected 1 key ref, got %d", len(got.Keys))
	}
	if got.RateLimits.MessagesPerMinute != 120 {
		t.Fatalf("expected 120 msg/min, got %d", got.RateLimits.MessagesPerMinute)
	}
}
