package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	var raw map[string]any
	_ = json.Unmarshal(res.Stdout, &raw)
	st.StatusKnown = true
	st.Authenticated = true
	if v, ok := raw["authenticated"].(bool); ok {
		st.Authenticated = v
	}
	if v, ok := raw["loggedIn"].(bool); ok {
		st.Authenticated = v
	}
	return st, nil
}

func (s *Service) authData(st cursorAuthStorage, fileName string) (pluginapi.AuthData, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return pluginapi.AuthData{}, err
	}
	return pluginapi.AuthData{Provider: providerID, ID: "cursor-cli", FileName: fileName, Label: "Cursor Agent CLI", StorageJSON: b, Metadata: map[string]any{"status_known": st.StatusKnown, "authenticated": st.Authenticated}, Attributes: map[string]string{"boundary": "official-cursor-agent-cli", "secrets": "not-read-by-plugin"}, NextRefreshAfter: s.now().Add(5 * time.Minute)}, nil
}

func unsupportedAuth() error {
	return statusError("unsupported_auth", "Cursor plugin uses the official Cursor Agent CLI browser-login session and does not parse or store secrets", http.StatusUnprocessableEntity)
}
