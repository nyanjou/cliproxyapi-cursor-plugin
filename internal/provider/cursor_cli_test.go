package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyanjou/cliproxyapi-cursor-plugin/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type recordingHost struct {
	mu     sync.Mutex
	emits  [][]byte
	closed string
}

func (h *recordingHost) Emit(_ context.Context, _ string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emits = append(h.emits, append([]byte(nil), payload...))
	return nil
}
func (h *recordingHost) CloseOutput(_ context.Context, _ string, message string) {
	h.mu.Lock()
	h.closed = message
	h.mu.Unlock()
}
func (h *recordingHost) Do(context.Context, string, transport.Request) (transport.Response, error) {
	return transport.Response{}, nil
}
func (h *recordingHost) OpenStream(context.Context, string, transport.Request) (transport.Stream, error) {
	return transport.Stream{}, nil
}
func (h *recordingHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	return transport.StreamChunk{}, nil
}
func (h *recordingHost) CloseStream(context.Context, string) error { return nil }

func fakeAgent(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestService(t *testing.T, agent string) *Service {
	t.Helper()
	h := &recordingHost{}
	s := New(h)
	s.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	cfg := DefaultConfig()
	cfg.ExecutablePath = agent
	cfg.Workspace = t.TempDir()
	cfg.TimeoutSeconds = 2
	cfg.MaxPromptBytes = 20000
	cfg.ModelCacheTTLSeconds = 1
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustYAML(t *testing.T, cfg Config) []byte {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDiscoverModelsFromAgentModelsFiltersPrefixes(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then
  printf '%s\n' 'Available models' '' 'auto - Auto (default)' 'gpt-5.2 - GPT 5.2' 'claude-sonnet-5 - Claude Sonnet' 'not a valid stray line'
  exit 0
fi
exit 64
`)
	s := newTestService(t, agent)
	cfg := s.Config()
	cfg.ExcludedModelPrefixes = []string{"claude-"}
	cfg.ModelPrefix = "cursor/"
	_ = s.Configure(mustYAML(t, cfg))
	resp, err := s.ModelsForAuth(context.Background(), "", pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if resp.Provider != "cursor" {
		t.Fatalf("provider=%q", resp.Provider)
	}
	got := []string{}
	for _, m := range resp.Models {
		got = append(got, m.ID)
	}
	if strings.Join(got, ",") != "cursor/auto,cursor/gpt-5.2" {
		t.Fatalf("models=%v", got)
	}
}

func TestExecuteJSONReturnsOpenAIResponsesPayloadAndUsesArgv(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
case "$*" in *'bad;touch /tmp/pwned'*) echo injection-arg >&2; exit 65;; esac
last=""
for arg in "$@"; do last="$arg"; done
case "$*" in *'--output-format json'*) printf '{"result":"exact answer","usage":{"input_tokens":3,"output_tokens":2}}\n'; exit 0;; esac
exit 64
`)
	s := newTestService(t, agent)
	req := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "bad;touch /tmp/pwned", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"Say hi"}`)}}
	if _, err := s.Execute(context.Background(), req); err == nil {
		t.Fatal("expected model allowlist/catalog failure for argv injection model")
	}
	req.Model = "auto"
	resp, err := s.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "exact answer" {
		t.Fatalf("text=%q payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 3 {
		t.Fatalf("usage not canonical: %s", resp.Payload)
	}
}

func TestInferenceArgvDisablesCursorSandbox(t *testing.T) {
	argvPath := filepath.Join(t.TempDir(), "argv.json")
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
python3 - "$@" <<'PY' > "`+argvPath+`"
import json, sys
json.dump(sys.argv[1:], sys.stdout)
PY
printf '{"result":"ok","usage":{"input_tokens":1,"output_tokens":2}}\n'
`)
	s := newTestService(t, agent)
	_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("argv json: %v: %s", err, raw)
	}
	for i, arg := range argv {
		if arg == "--sandbox" {
			if i+1 >= len(argv) || argv[i+1] != "disabled" {
				t.Fatalf("--sandbox argv = %v, want disabled", argv)
			}
			return
		}
	}
	t.Fatalf("argv omitted --sandbox disabled: %v", argv)
}

func TestExecuteRejectsToolSchemasTruthfully(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"tools":[{"type":"function","name":"x"}],"input":"hi"}`)}})
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected unsupported tools error, got %v", err)
	}
}

func TestStreamBuffersValidatedJSONIntoResponsesSSE(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
case "$*" in *'--sandbox enabled'*) echo sandbox-enabled >&2; exit 65;; esac
case "$*" in *'--sandbox disabled'*) ;; *) echo sandbox-disabled-missing >&2; exit 66;; esac
case "$*" in *'--print --output-format json'*) ;; *) echo json-flags-missing >&2; exit 67;; esac
case "$*" in *'stream-json'*) echo stream-json-not-allowed >&2; exit 68;; esac
printf '%s\n' '{"type":"result","result":"Hello!","usage":{"inputTokens":3,"outputTokens":2}}'
`)
	s := newTestService(t, agent)
	stream, err := s.ExecuteStream(context.Background(), ExecuteRequest{StreamID: "s1", ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", Format: "openai-response", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ct := stream.Headers.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
	joined := string(joinStreamChunks(stream.Chunks))
	for _, want := range []string{"event: response.created", "event: response.output_text.delta", "event: response.completed", "Hello!", `"input_tokens":3`, `"output_tokens":2`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream missing %q: %s", want, joined)
		}
	}
}

func joinStreamChunks(chunks []pluginapi.ExecutorStreamChunk) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c.Payload...)
	}
	return out
}

func joinBytes(chunks [][]byte) []byte {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}

func TestCursorJSONResultUsesRedactedStderrWhenStdoutIsEmpty(t *testing.T) {
	_, err := parseCursorJSONResult(agentResult{Stderr: []byte("Authorization: Bearer secret-token-12345")})
	if err == nil {
		t.Fatal("expected empty stdout error")
	}
	if strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("stderr was not safely surfaced: %v", err)
	}
}

func TestBoundsTimeoutAndRedaction(t *testing.T) {
	t.Run("request bound", func(t *testing.T) {
		s := newTestService(t, fakeAgent(t, `if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
exit 0`))
		cfg := s.Config()
		cfg.MaxPromptBytes = 1024
		_ = s.Configure(mustYAML(t, cfg))
		_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"` + strings.Repeat("x", 2000) + `"}`)}})
		if err == nil || !strings.Contains(err.Error(), "prompt") {
			t.Fatalf("expected prompt size error, got %v", err)
		}
	})
	t.Run("stderr redacted", func(t *testing.T) {
		agent := fakeAgent(t, `if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
echo 'Authorization: Bearer secret-token-12345' >&2; exit 2`)
		s := newTestService(t, agent)
		_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("not redacted: %v", err)
		}
	})
}

func TestAuthStatusFailClosedParsing(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		known bool
		auth  bool
	}{
		{"empty", ``, false, false},
		{"malformed", `{`, false, false},
		{"authenticated", `{"isAuthenticated":true}`, true, true},
		{"loggedIn false", `{"loggedIn":false}`, true, false},
		{"status authenticated", `{"status":"authenticated"}`, true, true},
		{"status unauthenticated", `{"status":"login required"}`, true, false},
		{"ambiguous conflict", `{"authenticated":true,"loggedIn":false}`, true, false},
		{"unknown object", `{"account":"person@example.test"}`, false, false},
		{"safe text true", `Authenticated`, true, true},
		{"identity text ambiguous", `Authenticated as person@example.test`, false, false},
		{"safe text false", `Not logged in`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			known, auth := parseCursorAuthStatus([]byte(tc.out))
			if known != tc.known || auth != tc.auth {
				t.Fatalf("parse=%v,%v want %v,%v", known, auth, tc.known, tc.auth)
			}
		})
	}
}

func TestExecuteRejectsUnsupportedContentBeforeAgentInvocation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	s := newTestService(t, fakeAgent(t, `echo invoked > "`+marker+`"
exit 0`))
	cases := []string{
		`{"input":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,xx"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"xx"}}]}]}`,
		`{"input":"hi","attachments":[{"file_id":"file-1"}]}`,
		`{"input":[{"type":"input_audio","audio":"xx"}]}`,
	}
	for _, raw := range cases {
		_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(raw)}})
		if err == nil || !strings.Contains(err.Error(), "text-only") {
			t.Fatalf("expected unsupported_content for %s, got %v", raw, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("agent invoked despite rejected content")
	}
}

func TestExecuteRejectsExternalToolShapesBeforeAgentInvocation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	s := newTestService(t, fakeAgent(t, `echo invoked > "`+marker+`"
exit 0`))
	cases := []string{
		`{"input":"hi","functions":[{"name":"x"}]}`,
		`{"input":"hi","tool_choice":"auto"}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"1"}]}]}`,
		`{"messages":[{"role":"assistant","function_call":{"name":"x"}}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"1"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1"}]}]}`,
		`{"input":[{"type":"server_tool_use","name":"web_search"}]}`,
		`{"input":"hi","mcp_tool_call":{"name":"x"}}`,
	}
	for _, raw := range cases {
		_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(raw)}})
		if err == nil || !strings.Contains(err.Error(), "external tool/function") {
			t.Fatalf("expected unsupported_tools for %s, got %v", raw, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("agent invoked despite rejected tool shape")
	}
}

func TestRunAgentKillsProcessGroupOnTimeout(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survivor")
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
(sleep 3; echo survived > "`+marker+`") &
wait
`)
	s := newTestService(t, agent)
	cfg := s.Config()
	cfg.TimeoutSeconds = 1
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout did not return promptly: %s", elapsed)
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived process-group kill")
	}
}

func TestRunAgentUsesFreshPrivateWorkspaceAndCleansIt(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "workspaces")
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
printf '%s\n' "$PWD" >> "`+logPath+`"
printf '{"result":"ok"}\n'
`)
	s := newTestService(t, agent)
	for i := 0; i < 2; i++ {
		_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Fatalf("expected two distinct invocation workspaces, got %q", raw)
	}
	root, err := filepath.EvalSymlinks(s.Config().Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range lines {
		parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
		if err != nil {
			t.Fatal(err)
		}
		if parent != root {
			t.Fatalf("workspace %q is not under configured root %q", dir, s.Config().Workspace)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("workspace %q was not cleaned up", dir)
		}
	}
}

func TestStreamMalformedAndMissingResultAreErrors(t *testing.T) {
	host := &recordingHost{}
	err := streamCursorJSON(context.Background(), host, "s", "openai-response", "auto", 1, strings.NewReader("{not-json\n"))
	if err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("expected parse error, got %v", err)
	}
	host = &recordingHost{}
	err = streamCursorJSON(context.Background(), host, "s", "openai-response", "auto", 1, strings.NewReader("{\"type\":\"text\",\"text\":\"hi\"}\n"))
	if err == nil || !strings.Contains(err.Error(), "terminal result") {
		t.Fatalf("expected missing result error, got %v", err)
	}
}
