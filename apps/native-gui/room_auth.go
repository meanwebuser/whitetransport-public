package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	wtmobile "github.com/meanwebuser/whitetransport/core/mobile"
)

// roomAuthStatus is the only room-auth result exposed to the Wails frontend.
// Credential values remain inside the helper stdout pipe and local runtime store.
type roomAuthStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type roomAuthPayload struct {
	Platform     string `json:"platform"`
	AccessToken  string `json:"access_token"`
	CookieHeader string `json:"cookie_header"`
}

// localSessionRuntime is the desktop seam for the shared mobile LocalSession
// contract. It receives the session only as an in-memory JSON argument.
type localSessionRuntime interface {
	StartTransportWithLocalSession(configJSON, localSessionJSON string) error
	StopTransport()
}

type embeddedRoomRuntime struct{}

func newEmbeddedRoomRuntime() localSessionRuntime { return embeddedRoomRuntime{} }

func (embeddedRoomRuntime) StartTransportWithLocalSession(configJSON, localSessionJSON string) error {
	return wtmobile.StartTransportWithLocalSession(configJSON, localSessionJSON)
}

func (embeddedRoomRuntime) StopTransport() { wtmobile.StopTransport() }

// BeginRoomAuth launches the platform-owned MiniAuth helper asynchronously.
func (a *App) BeginRoomAuth() (roomAuthStatus, error) {
	helper, err := roomAuthHelperPath()
	if err != nil {
		return roomAuthStatus{}, err
	}
	a.roomAuthMu.Lock()
	if a.roomAuth.State == "opening" {
		status := a.roomAuth
		a.roomAuthMu.Unlock()
		return status, nil
	}
	a.roomAuth = roomAuthStatus{State: "opening", Message: "Открываю встроенный вход WB Stream."}
	status := a.roomAuth
	a.roomAuthMu.Unlock()
	go a.waitForRoomAuth(helper)
	return status, nil
}

// GetRoomAuthStatus returns a credential-free status suitable for the UI.
func (a *App) GetRoomAuthStatus() roomAuthStatus {
	a.roomAuthMu.Lock()
	defer a.roomAuthMu.Unlock()
	return a.roomAuth
}

func (a *App) waitForRoomAuth(helper string) {
	output, err := exec.CommandContext(context.Background(), helper).Output()
	if err != nil {
		a.setRoomAuthStatus(roomAuthStatus{State: "error", Message: "Встроенный вход завершился без локальной сессии."})
		return
	}
	payload, err := parseRoomAuthPayload(output)
	if err != nil {
		a.setRoomAuthStatus(roomAuthStatus{State: "error", Message: "Встроенный вход вернул неполную локальную сессию."})
		return
	}
	if err := a.startRoomRuntime(payload); err != nil {
		a.setRoomAuthStatus(roomAuthStatus{State: "error", Message: "Не удалось запустить локальное транспортное ядро."})
		return
	}
	a.setRoomAuthStatus(roomAuthStatus{State: "ready", Message: "Локальная сессия готова для создания комнаты."})
}

func (a *App) setRoomAuthStatus(status roomAuthStatus) {
	a.roomAuthMu.Lock()
	a.roomAuth = status
	a.roomAuthMu.Unlock()
}

func parseRoomAuthPayload(raw []byte) (roomAuthPayload, error) {
	var payload roomAuthPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return roomAuthPayload{}, fmt.Errorf("decode room auth result: %w", err)
	}
	payload.Platform = strings.ToLower(strings.TrimSpace(payload.Platform))
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.CookieHeader = strings.TrimSpace(payload.CookieHeader)
	if payload.Platform != "wbstream" || payload.AccessToken == "" || payload.CookieHeader == "" {
		return roomAuthPayload{}, fmt.Errorf("invalid room auth result")
	}
	return payload, nil
}

// startRoomRuntime stops the config-file daemon and moves exactly one local
// provider session into the embedded Go runtime. The session is never added to
// client-tokens.json, TokenStore, environment, argv, or a generated config.
func (a *App) startRoomRuntime(payload roomAuthPayload) error {
	localSessionJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode local room session: %w", err)
	}
	configCandidate, ok := a.resources.FirstFoundCandidate(guiruntime.ResourceDaemonConfig)
	if !ok || strings.TrimSpace(configCandidate.Target) == "" {
		return fmt.Errorf("local room runtime requires a managed daemon config")
	}
	configJSON, err := os.ReadFile(configCandidate.Target)
	if err != nil {
		return fmt.Errorf("read base daemon config: %w", err)
	}

	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.roomRuntime == nil {
		return fmt.Errorf("local room runtime is unavailable")
	}
	if a.supervisor != nil {
		if err := a.supervisor.Stop(a.context()); err != nil {
			return fmt.Errorf("stop config-file daemon before local room runtime: %w", err)
		}
	}
	if err := a.roomRuntime.StartTransportWithLocalSession(string(configJSON), string(localSessionJSON)); err != nil {
		if a.supervisor != nil {
			_, _ = a.supervisor.Restart(a.context())
		}
		return fmt.Errorf("start local room runtime: %w", err)
	}
	a.roomRuntimeActive = true
	return nil
}
