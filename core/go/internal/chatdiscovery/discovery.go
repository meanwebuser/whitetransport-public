package chatdiscovery

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const vkAPIVersion = "5.199"
const okGraphBase = "https://api.ok.ru/graph"

// DiscoveredChat is one discovered VK or OK chat/peer.
type DiscoveredChat struct {
	Platform string `json:"platform"`
	PeerID   string `json:"peer_id"`
	Type     string `json:"type"`     // "user", "chat", "group", "GROUP_CHAT"
	Title    string `json:"title"`    // Display name if available
	Account  string `json:"account"`  // Token account ID
}

// ChatAllocation assigns chats to roles.
type ChatAllocation struct {
	Discovery []string `json:"discovery"`
	Control   []string `json:"control"`
	Logs      []string `json:"logs"`
	Admin     []string `json:"admin"`
	Egress    []string `json:"egress"`
}

// AllocationResult is the full discovery + allocation output.
type AllocationResult struct {
	VK    map[string]ChatAllocation `json:"vk"`
	OK    map[string]ChatAllocation `json:"ok"`
	Total int                       `json:"total"`
}

// DiscoverVKChats calls VK API to list all conversations for the given token.
func DiscoverVKChats(token string, accountID string) ([]DiscoveredChat, error) {
	var allChats []DiscoveredChat
	offset := 0
	for {
		apiURL := fmt.Sprintf("https://api.vk.com/method/messages.getConversations?count=200&offset=%d&extended=1&access_token=%s&v=%s",
			offset, url.QueryEscape(token), vkAPIVersion)
		body, err := httpGetJSON(apiURL)
		if err != nil {
			log.Printf("chatdiscovery: VK api call failed: %v", err)
			return nil, fmt.Errorf("vk conversations: %w", err)
		}
		if errVal, ok := body["error"]; ok {
			errMap, _ := errVal.(map[string]any)
			return nil, fmt.Errorf("vk api error: %v", errMap["error_msg"])
		}
		resp, _ := body["response"].(map[string]any)
		items, _ := resp["items"].([]any)
		if len(items) == 0 {
			break
		}
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			conv, _ := item["conversation"].(map[string]any)
			peer, _ := conv["peer"].(map[string]any)
			peerID := peerIDToString(peer["id"])
			peerType, _ := peer["type"].(string)
			if peerID == "" {
				continue
			}

			title := ""
			if profile, ok := item["profile"].(map[string]any); ok {
				first, _ := profile["first_name"].(string)
				last, _ := profile["last_name"].(string)
				title = strings.TrimSpace(first + " " + last)
			}
			if group, ok := item["group"].(map[string]any); ok {
				title, _ = group["name"].(string)
			}
			if peerType == "chat" {
				title, _ = conv["chat_settings"].(map[string]any)["title"].(string)
			}

			allChats = append(allChats, DiscoveredChat{
				Platform: "vk",
				PeerID:   peerID,
				Type:     peerType,
				Title:    title,
				Account:  accountID,
			})
			log.Printf("chatdiscovery: VK peer id=%s type=%s", peerID, peerType)
		}
		if len(items) < 200 {
			break
		}
		offset += 200
	}
	return allChats, nil
}

// DiscoverOKChats calls OK Graph API to list all chats.
func DiscoverOKChats(token string, accountID string) ([]DiscoveredChat, error) {
	var allChats []DiscoveredChat
	apiURL := fmt.Sprintf("%s/me/chats?access_token=%s&limit=100",
		okGraphBase, url.QueryEscape(token))
	body, err := httpGetJSON(apiURL)
	if err != nil {
		return nil, fmt.Errorf("ok chats: %w", err)
	}
	chats, _ := body["chats"].([]any)
	for _, raw := range chats {
		chat, _ := raw.(map[string]any)
		chatID, _ := chat["chat_id"].(string)
		chatType, _ := chat["type"].(string)
		title, _ := chat["title"].(string)
		allChats = append(allChats, DiscoveredChat{
			Platform: "ok",
			PeerID:   chatID,
			Type:     chatType,
			Title:    title,
			Account:  accountID,
		})
	}
	return allChats, nil
}

// AllocateChats distributes discovered chats across roles.
// Strategy:
//   - Sort by peer_id for deterministic allocation
//   - First chat → discovery
//   - Second chat → control
//   - Third chat → logs
//   - Fourth chat → admin
//   - All remaining → egress pool
func AllocateChats(chats []DiscoveredChat) ChatAllocation {
	if len(chats) == 0 {
		return ChatAllocation{}
	}

	sorted := make([]DiscoveredChat, len(chats))
	copy(sorted, chats)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PeerID < sorted[j].PeerID
	})

	alloc := ChatAllocation{}
	roles := []func(string){func(id string) { alloc.Discovery = append(alloc.Discovery, id) }}
	if len(sorted) > 1 {
		roles = append(roles, func(id string) { alloc.Control = append(alloc.Control, id) })
	}
	if len(sorted) > 2 {
		roles = append(roles, func(id string) { alloc.Logs = append(alloc.Logs, id) })
	}
	if len(sorted) > 3 {
		roles = append(roles, func(id string) { alloc.Admin = append(alloc.Admin, id) })
	}

	for i, chat := range sorted {
		if i < len(roles) {
			roles[i](chat.PeerID)
		} else {
			alloc.Egress = append(alloc.Egress, chat.PeerID)
		}
	}

	if len(alloc.Egress) == 0 && len(sorted) > 0 {
		alloc.Egress = append(alloc.Egress, sorted[0].PeerID)
	}
	return alloc
}

// DiscoverAll discovers VK and OK chats for all given tokens and allocates them.
func DiscoverAll(vkTokens map[string]string, okTokens map[string]string) (*AllocationResult, error) {
	result := &AllocationResult{
		VK:    make(map[string]ChatAllocation),
		OK:    make(map[string]ChatAllocation),
		Total: 0,
	}

	for accountID, token := range vkTokens {
		chats, err := DiscoverVKChats(token, accountID)
		if err != nil {
			log.Printf("chatdiscovery: VK %s failed: %v", accountID, err)
			continue
		}
		log.Printf("chatdiscovery: VK %s found %d peers", accountID, len(chats))
		result.VK[accountID] = AllocateChats(chats)
		log.Printf("chatdiscovery: VK %s alloc: disc=%v ctrl=%v logs=%v egress=%v",
			accountID,
			result.VK[accountID].Discovery,
			result.VK[accountID].Control,
			result.VK[accountID].Logs,
			result.VK[accountID].Egress)
		result.Total += len(chats)
	}

	for accountID, token := range okTokens {
		chats, err := DiscoverOKChats(token, accountID)
		if err != nil {
			log.Printf("chatdiscovery: OK %s failed: %v", accountID, err)
			continue
		}
		log.Printf("chatdiscovery: OK %s found %d chats", accountID, len(chats))
		result.OK[accountID] = AllocateChats(chats)
		result.Total += len(chats)
	}

	return result, nil
}

func httpGetJSON(rawURL string) (map[string]any, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return result, nil
}

func peerIDToString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", val)
	case string:
		return val
	default:
		return fmt.Sprintf("%v", v)
	}
}
