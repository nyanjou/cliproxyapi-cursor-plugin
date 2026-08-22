package provider

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestLiveOfficialCursorAgentCLI(t *testing.T) {
	if os.Getenv("RUN_CURSOR_LIVE") != "1" {
		t.Skip("set RUN_CURSOR_LIVE=1 to exercise the authenticated official Cursor Agent CLI")
	}
	agent := os.Getenv("CURSOR_AGENT_PATH")
	if agent == "" {
		agent = defaultAgentPath
	}
	host := &recordingHost{}
	s := New(host)
	cfg := DefaultConfig()
	cfg.ExecutablePath = agent
	cfg.Workspace = t.TempDir()
	cfg.TimeoutSeconds = 180
	cfg.ModelCacheTTLSeconds = 0
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	st, err := s.statusStorage(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.StatusKnown || !st.Authenticated {
		t.Fatalf("unexpected status: known=%v authenticated=%v", st.StatusKnown, st.Authenticated)
	}
	models, err := s.ModelsForAuth(context.Background(), "", pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models.Models) == 0 {
		t.Fatal("no models discovered")
	}
	req := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"Respond with exactly: NYANJOU_CURSOR_OK"}`)}}
	resp, err := s.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := strings.TrimSpace(gjson.GetBytes(resp.Payload, "output.0.content.0.text").String()); got != "NYANJOU_CURSOR_OK" {
		t.Fatalf("unexpected exact text %q", got)
	}
	streamReq := ExecuteRequest{StreamID: "live", ExecutorRequest: pluginapi.ExecutorRequest{Model: "auto", SourceFormat: "openai-response", OriginalRequest: []byte(`{"input":"Respond with exactly: NYANJOU_STREAM_OK"}`)}}
	if _, err := s.ExecuteStream(context.Background(), streamReq); err != nil {
		t.Fatalf("stream: %v", err)
	}
	deadline := time.Now().Add(190 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		closed := host.closed
		emits := append([][]byte(nil), host.emits...)
		host.mu.Unlock()
		if closed != "" {
			t.Fatalf("stream closed with error: %s", closed)
		}
		joined := string(joinBytes(emits))
		if strings.Contains(joined, "NYANJOU_STREAM_OK") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, _ := json.Marshal(host.emits)
	t.Fatalf("stream exact text not observed in %s", b)
}
