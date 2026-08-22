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

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/snupai/cliproxyapi-cursor-plugin/internal/transport"
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

func TestExecuteRejectsToolSchemasTruthfully(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	_, err := s.Execute(context.Background(), ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"tools":[{"type":"function","name":"x"}],"input":"hi"}`)}})
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected unsupported tools error, got %v", err)
	}
}

func TestStreamDeduplicatesPartialsAndUsesTerminalResult(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "models" ]; then echo 'auto - Auto'; exit 0; fi
printf '%s\n' '{"type":"text","text":"Hel"}' '{"type":"text","text":"Hello"}' '{"type":"text","text":"Hello"}' '{"type":"result","result":"Hello!","usage":{"input_tokens":1,"output_tokens":1}}'
`)
	host := &recordingHost{}
	s := New(host)
	cfg := DefaultConfig()
	cfg.ExecutablePath = agent
	cfg.Workspace = t.TempDir()
	cfg.TimeoutSeconds = 2
	cfg.MaxPromptBytes = 20000
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	headers, err := s.ExecuteStream(context.Background(), ExecuteRequest{StreamID: "s1", ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"hi"}`)}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ct := headers.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
	time.Sleep(300 * time.Millisecond)
	host.mu.Lock()
	defer host.mu.Unlock()
	joined := string(joinBytes(host.emits))
	if strings.Count(joined, `"delta":"Hel"`) != 1 || strings.Count(joined, `"delta":"lo"`) != 1 {
		t.Fatalf("dedupe failed emits=%s", joined)
	}
	if !strings.Contains(joined, "Hello!") {
		t.Fatalf("terminal result missing emits=%s", joined)
	}
	if host.closed != "" {
		t.Fatalf("closed with error %q", host.closed)
	}
}

func joinBytes(chunks [][]byte) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
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
