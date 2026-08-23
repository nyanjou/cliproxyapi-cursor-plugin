package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type cursorAuthStorage struct {
	Type           string `json:"type"`
	ExecutablePath string `json:"executable_path,omitempty"`
	Email          string `json:"email,omitempty"`
	Account        string `json:"account,omitempty"`
	Tier           string `json:"tier,omitempty"`
	Version        string `json:"version,omitempty"`
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
	return authParseResponse(auth), nil
}

var cursorApprovalURLRE = regexp.MustCompile(`https://(?:www\.)?cursor\.com/[^\s"'<>]+`)

func (s *Service) StartLogin(ctx context.Context, _ string, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	cfg := s.Config()
	if _, err := resolveAgentExecutable(cfg); err != nil {
		setup, setupErr := setupURLFromBase(req.BaseURL)
		if setupErr != nil {
			return pluginapi.AuthLoginStartResponse{}, setupErr
		}
		return pluginapi.AuthLoginStartResponse{Provider: providerID, URL: setup, State: "setup-required", ExpiresAt: s.now().Add(15 * time.Minute), Metadata: map[string]any{"setup_required": true, "message": "Install the official Cursor Agent CLI with explicit confirmation, then continue login."}}, nil
	}
	if st, err := s.statusStorage(ctx); err == nil && st.StatusKnown && st.Authenticated {
		state := fmt.Sprintf("cursor-login-%d", s.now().UnixNano())
		expires := s.now().Add(time.Minute)
		s.loginMu.Lock()
		s.logins[state] = &loginSession{startedAt: s.now(), expiresAt: expires, done: true}
		s.loginMu.Unlock()
		return pluginapi.AuthLoginStartResponse{Provider: providerID, State: state, ExpiresAt: expires, Metadata: map[string]any{"authenticated": true, "message": "Cursor CLI is already authenticated."}}, nil
	}
	state := fmt.Sprintf("cursor-login-%d", s.now().UnixNano())
	expires := s.now().Add(15 * time.Minute)
	urlCh := make(chan string, 1)
	s.loginMu.Lock()
	s.logins[state] = &loginSession{startedAt: s.now(), expiresAt: expires}
	s.loginMu.Unlock()
	go func() {
		out, err := s.runAgentLoginStreaming(ctx, cfg, state, urlCh)
		s.loginMu.Lock()
		if sess := s.logins[state]; sess != nil {
			sess.output = strings.TrimSpace(string(out.Stdout))
			if err != nil {
				sess.err = err.Error()
			}
			sess.done = true
		}
		s.loginMu.Unlock()
	}()
	approval := ""
	select {
	case approval = <-urlCh:
	case <-time.After(1200 * time.Millisecond):
	}
	return pluginapi.AuthLoginStartResponse{Provider: providerID, URL: approval, State: state, ExpiresAt: expires, Metadata: map[string]any{"approval_url": approval, "message": "Approve the Cursor URL, then polling will complete after the CLI reports authenticated."}}, nil
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
		if sess.approvalURL != "" {
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Approve Cursor login URL: " + sess.approvalURL}, nil
		}
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for Cursor CLI login approval URL"}, nil
	}
	if sess.err != "" {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: sess.err}, nil
	}
	st, err := s.statusStorage(ctx)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}, nil
	}
	if !st.StatusKnown || !st.Authenticated {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Cursor login process exited, but authenticated status is not yet confirmed"}, nil
	}
	auth, err := s.authData(st, "")
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, err
	}
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Message: "Cursor CLI login available", Auth: auth, Auths: []pluginapi.AuthData{auth}}, nil
}

func (s *Service) runAgentLoginStreaming(ctx context.Context, cfg Config, state string, urlCh chan<- string) (agentResult, error) {
	if !cfg.Enabled {
		return agentResult{}, statusError("plugin_disabled", "Cursor plugin is disabled", http.StatusServiceUnavailable)
	}
	workspace, cleanup, err := invocationWorkspace(cfg.Workspace)
	if err != nil {
		return agentResult{}, err
	}
	defer cleanup()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return agentResult{}, ctx.Err()
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	exe, err := resolveAgentExecutable(cfg)
	if err != nil {
		return agentResult{}, err
	}
	cmd := exec.CommandContext(runCtx, exe, "login")
	cmd.Dir = workspace
	cmd.Env = filteredEnv(cfg.EnvironmentAllowlist, true)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return agentResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return agentResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return agentResult{}, fmt.Errorf("start Cursor agent CLI: %w", err)
	}
	var stdout, stderr limitedBuffer
	stdout.limit = cfg.MaxStdoutBytes
	stderr.limit = cfg.MaxStderrBytes
	var foundOnce sync.Once
	consume := func(r io.Reader, buf *limitedBuffer) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024), 64*1024)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = buf.Write([]byte(line + "\n"))
			if u := safeApprovalURL(line); u != "" {
				foundOnce.Do(func() {
					s.loginMu.Lock()
					if sess := s.logins[state]; sess != nil {
						sess.approvalURL = u
					}
					s.loginMu.Unlock()
					select {
					case urlCh <- u:
					default:
					}
				})
			}
		}
	}
	doneRead := make(chan struct{}, 2)
	go func() { consume(stdoutPipe, &stdout); doneRead <- struct{}{} }()
	go func() { consume(stderrPipe, &stderr); doneRead <- struct{}{} }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		killProcessGroup(cmd)
		<-done
		return agentResult{Stdout: append([]byte(nil), stdout.Bytes()...), Stderr: append([]byte(nil), stderr.Bytes()...)}, statusError("cursor_timeout", "Cursor agent CLI login timed out", http.StatusGatewayTimeout)
	}
	<-doneRead
	<-doneRead
	res := agentResult{Stdout: append([]byte(nil), stdout.Bytes()...), Stderr: append([]byte(nil), stderr.Bytes()...)}
	if waitErr != nil {
		msg := strings.TrimSpace(redactCursorError(string(res.Stderr)))
		if msg == "" {
			msg = strings.TrimSpace(redactCursorError(string(res.Stdout)))
		}
		return res, statusError("cursor_agent_error", "Cursor agent CLI login failed: "+msg, http.StatusBadGateway)
	}
	return res, nil
}

func safeApprovalURL(text string) string {
	m := cursorApprovalURLRE.FindAllString(text, -1)
	if len(m) != 1 {
		return ""
	}
	u, err := url.Parse(m[0])
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := u.Hostname()
	if host != "cursor.com" && host != "www.cursor.com" {
		return ""
	}
	return u.String()
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
	s.enrichStorageFromAbout(ctx, cfg, &st)
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
	st.Type = providerID
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "cursor-cli.json"
	}
	b, err := json.Marshal(st)
	if err != nil {
		return pluginapi.AuthData{}, err
	}
	label := firstNonEmpty(st.Email, st.Account, "Cursor Agent CLI")
	metadata := map[string]any{
		"type":           providerID,
		"auth_kind":      "oauth",
		"status_known":   st.StatusKnown,
		"authenticated":  st.Authenticated,
		"quota_source":   "CPA observed requests and official Cursor CLI status/about metadata",
		"quota_exposure": "official Cursor CLI does not expose numeric remaining subscription quota",
	}
	if st.Email != "" {
		metadata["email"] = st.Email
	}
	if st.Account != "" {
		metadata["account"] = st.Account
	}
	if st.Tier != "" {
		metadata["tier"] = st.Tier
		metadata["plan"] = st.Tier
	}
	if st.Version != "" {
		metadata["version"] = st.Version
		metadata["cli_version"] = st.Version
	}
	attributes := map[string]string{
		"auth_kind": "oauth",
		"boundary":  "official-cursor-agent-cli",
		"secrets":   "not-read-by-plugin",
	}
	if st.Email != "" {
		attributes["account_email"] = st.Email
	}
	return pluginapi.AuthData{Provider: providerID, ID: "cursor-cli", FileName: fileName, Label: label, StorageJSON: b, Metadata: metadata, Attributes: attributes, NextRefreshAfter: s.now().Add(5 * time.Minute)}, nil
}

func authParseResponse(auth pluginapi.AuthData) pluginapi.AuthParseResponse {
	return pluginapi.AuthParseResponse{Handled: true, Auth: auth, Auths: []pluginapi.AuthData{auth}}
}

func (s *Service) enrichStorageFromAbout(ctx context.Context, cfg Config, st *cursorAuthStorage) {
	if st == nil {
		return
	}
	result, err := s.runAgent(ctx, cfg, []string{"about", "--format", "json"}, nil, false)
	if err != nil {
		return
	}
	about, err := parseCursorAbout(result.Stdout)
	if err != nil {
		return
	}
	if about.Account != "" {
		st.Account = about.Account
	}
	if about.Email != "" {
		st.Email = about.Email
	} else if st.Email == "" && looksLikeEmail(about.Account) {
		st.Email = about.Account
	}
	if about.Tier != "" {
		st.Tier = about.Tier
	}
	if about.Version != "" {
		st.Version = about.Version
	}
}

type cursorAboutInfo struct {
	Account string
	Email   string
	Tier    string
	Version string
}

func parseCursorAbout(raw []byte) (cursorAboutInfo, error) {
	var decoded map[string]any
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(string(raw))))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return cursorAboutInfo{}, fmt.Errorf("cursor agent about JSON was malformed")
	}
	account := firstSafeString(decoded, "account", "username", "name")
	email := firstSafeString(decoded, "userEmail", "user_email", "email")
	if email == "" {
		email = safeAccountEmail(decoded["account"])
	}
	if account == "" {
		account = safeAccount(decoded["account"])
	}
	return cursorAboutInfo{
		Account: account,
		Email:   email,
		Tier:    firstSafeString(decoded, "tier", "plan", "subscriptionTier", "subscription_tier"),
		Version: firstSafeString(decoded, "version", "cliVersion", "cli_version", "agentVersion", "agent_version", "cursorVersion", "cursor_version"),
	}, nil
}

func safeAccountEmail(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	return firstSafeString(m, "email")
}

func safeAccount(v any) string {
	s, _ := v.(string)
	if strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	return firstSafeString(m, "email", "username", "name")
}

func firstSafeString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, _ := raw[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "@") && !strings.ContainsAny(value, " \t\r\n")
}
