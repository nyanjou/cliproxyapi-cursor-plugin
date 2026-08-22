package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type cursorAuthStorage struct {
	Type           string `json:"type"`
	ExecutablePath string `json:"executable_path,omitempty"`
	StatusKnown    bool   `json:"status_known,omitempty"`
	Authenticated  bool   `json:"authenticated,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

func (s *Service) ParseAuth(req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if req.Provider != "" && !strings.EqualFold(req.Provider, providerID) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	st := cursorAuthStorage{Type: providerID, ExecutablePath: s.Config().ExecutablePath, UpdatedAt: s.now().UTC().Format(time.RFC3339)}
	if len(req.RawJSON) > 0 {
		_ = json.Unmarshal(req.RawJSON, &st)
	}
	auth, err := s.authData(st, req.FileName)
	if err != nil {
		return pluginapi.AuthParseResponse{}, err
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: auth}, nil
}

func (s *Service) StartLogin(ctx context.Context, _ string) (pluginapi.AuthLoginStartResponse, error) {
	cfg := s.Config()
	state := fmt.Sprintf("cursor-login-%d", s.now().UnixNano())
	expires := s.now().Add(15 * time.Minute)
	s.loginMu.Lock()
	s.logins[state] = &loginSession{startedAt: s.now(), expiresAt: expires}
	s.loginMu.Unlock()
	// NO_OPEN_BROWSER=1 makes the official CLI print an approval URL/state. We capture only bounded stdout/stderr.
	go func() {
		out, _ := s.runAgent(ctx, cfg, []string{"login"}, nil, true)
		s.loginMu.Lock()
		if sess := s.logins[state]; sess != nil {
			sess.output = strings.TrimSpace(string(out.Stdout))
			sess.done = true
		}
		s.loginMu.Unlock()
	}()
	return pluginapi.AuthLoginStartResponse{Provider: providerID, State: state, ExpiresAt: expires, Metadata: map[string]any{"message": "Run NO_OPEN_BROWSER=1 agent login approval flow; poll for the printed URL/state."}}, nil
}

func (s *Service) PollLogin(ctx context.Context, _ string, state string) (pluginapi.AuthLoginPollResponse, error) {
	s.loginMu.Lock()
	sess := s.logins[strings.TrimSpace(state)]
	s.loginMu.Unlock()
	if sess == nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "unknown Cursor login state"}, nil
	}
	if !s.now().Before(sess.expiresAt) {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Cursor login state expired"}, nil
	}
	if !sess.done {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for Cursor CLI login output"}, nil
	}
	st, err := s.statusStorage(ctx)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	auth, err := s.authData(st, "")
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, err
	}
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Message: "Cursor CLI login available", Auth: auth}, nil
}

func (s *Service) RefreshAuth(ctx context.Context, _ string, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	st, err := s.statusStorage(ctx)
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, err
	}
	auth, err := s.authData(st, "")
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, err
	}
	return pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: s.now().Add(5 * time.Minute)}, nil
}

func (s *Service) statusStorage(ctx context.Context) (cursorAuthStorage, error) {
	cfg := s.Config()
	st := cursorAuthStorage{Type: providerID, ExecutablePath: cfg.ExecutablePath, UpdatedAt: s.now().UTC().Format(time.RFC3339)}
	res, err := s.runAgent(ctx, cfg, []string{"status", "--format", "json"}, nil, false)
	if err != nil {
		res, err = s.runAgent(ctx, cfg, []string{"status"}, nil, false)
		if err != nil {
			return st, err
		}
	}
	known, authenticated := parseCursorAuthStatus(res.Stdout)
	st.StatusKnown = known
	st.Authenticated = authenticated
	return st, nil
}

func parseCursorAuthStatus(stdout []byte) (known bool, authenticated bool) {
	text := strings.TrimSpace(string(stdout))
	if text == "" {
		return false, false
	}
	var raw any
	if err := json.Unmarshal(stdout, &raw); err == nil {
		return parseCursorAuthJSON(raw)
	}
	return parseCursorAuthText(text)
}

func parseCursorAuthJSON(v any) (known bool, authenticated bool) {
	truths := []bool{}
	var walk func(any)
	walk = func(cur any) {
		switch x := cur.(type) {
		case map[string]any:
			for k, vv := range x {
				lk := strings.ToLower(strings.TrimSpace(k))
				switch lk {
				case "isauthenticated", "authenticated", "loggedin":
					if b, ok := vv.(bool); ok {
						truths = append(truths, b)
					}
				case "status":
					if s, ok := vv.(string); ok {
						if k, a := classifyAuthStatusString(s); k {
							truths = append(truths, a)
						}
					}
				}
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	walk(v)
	if len(truths) == 0 {
		return false, false
	}
	first := truths[0]
	for _, b := range truths[1:] {
		if b != first {
			return true, false
		}
	}
	return true, first
}

func parseCursorAuthText(text string) (known bool, authenticated bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k, a := classifyAuthStatusString(line); k {
			return true, a
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "not logged in") || strings.Contains(lower, "not authenticated") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "login required") {
			return true, false
		}
		if lower == "logged in" || lower == "authenticated" || lower == "status: authenticated" || lower == "status: logged in" || lower == "isauthenticated: true" || lower == "authenticated: true" || lower == "loggedin: true" {
			return true, true
		}
	}
	return false, false
}

func classifyAuthStatusString(value string) (known bool, authenticated bool) {
	s := strings.ToLower(strings.TrimSpace(value))
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.Join(strings.Fields(s), " ")
	switch s {
	case "authenticated", "logged-in", "logged in", "signed-in", "signed in", "ok", "true":
		return true, true
	case "unauthenticated", "not-authenticated", "not authenticated", "logged-out", "logged out", "not-logged-in", "not logged in", "signed-out", "signed out", "login-required", "login required", "false":
		return true, false
	default:
		return false, false
	}
}

func (s *Service) authData(st cursorAuthStorage, fileName string) (pluginapi.AuthData, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return pluginapi.AuthData{}, err
	}
	return pluginapi.AuthData{Provider: providerID, ID: "cursor-cli", FileName: fileName, Label: "Cursor Agent CLI", StorageJSON: b, Metadata: map[string]any{"status_known": st.StatusKnown, "authenticated": st.Authenticated}, Attributes: map[string]string{"boundary": "official-cursor-agent-cli", "secrets": "not-read-by-plugin"}, NextRefreshAfter: s.now().Add(5 * time.Minute)}, nil
}
