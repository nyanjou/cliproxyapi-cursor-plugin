package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/snupai/cliproxyapi-cursor-plugin/internal/redact"
)

type agentResult struct {
	Stdout []byte
	Stderr []byte
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = b.buf.Write(p[:remain])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

func ensureWorkspace(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("workspace must be an absolute path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create Cursor workspace: %w", err)
	}
	return nil
}

func (s *Service) runAgent(ctx context.Context, cfg Config, args []string, stdin []byte, login bool) (agentResult, error) {
	if !cfg.Enabled {
		return agentResult{}, statusError("plugin_disabled", "Cursor plugin is disabled", http.StatusServiceUnavailable)
	}
	if err := ensureWorkspace(cfg.Workspace); err != nil {
		return agentResult{}, err
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return agentResult{}, ctx.Err()
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	argv := append([]string(nil), args...)
	cmd := exec.CommandContext(runCtx, cfg.ExecutablePath, argv...)
	cmd.Dir = cfg.Workspace
	cmd.Env = filteredEnv(cfg.EnvironmentAllowlist, login)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr limitedBuffer
	stdout.limit = cfg.MaxStdoutBytes
	stderr.limit = cfg.MaxStderrBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	startErr := cmd.Start()
	if startErr != nil {
		return agentResult{}, fmt.Errorf("start Cursor agent CLI: %w", startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	err := <-done
	if runCtx.Err() != nil {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return agentResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, statusError("cursor_timeout", "Cursor agent CLI timed out", http.StatusGatewayTimeout)
	}
	res := agentResult{Stdout: append([]byte(nil), stdout.Bytes()...), Stderr: append([]byte(nil), stderr.Bytes()...)}
	if stdout.truncated {
		return res, statusError("cursor_stdout_limit", "Cursor agent CLI stdout exceeded configured limit", http.StatusBadGateway)
	}
	if err != nil {
		msg := strings.TrimSpace(redactCursorError(string(res.Stderr)))
		if msg == "" {
			msg = strings.TrimSpace(redactCursorError(string(res.Stdout)))
		}
		if msg == "" {
			msg = err.Error()
		}
		return res, statusError("cursor_agent_error", "Cursor agent CLI failed: "+msg, http.StatusBadGateway)
	}
	return res, nil
}

func filteredEnv(allow []string, login bool) []string {
	allowed := map[string]struct{}{}
	for _, name := range allow {
		allowed[name] = struct{}{}
	}
	out := []string{}
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, keep := allowed[name]; keep && !strings.EqualFold(name, "CURSOR_API_KEY") {
			out = append(out, kv)
		}
	}
	if login {
		out = append(out, "NO_OPEN_BROWSER=1")
	}
	out = append(out, "CURSOR_API_KEY=")
	return out
}

func redactCursorError(text string) string {
	text = redact.Text(text)
	for _, marker := range []string{"CURSOR_API_KEY", "Authorization", "Bearer ", ".cursor", "auth"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(marker)) {
			text = strings.ReplaceAll(text, marker, "[REDACTED]")
		}
	}
	return text
}

func cursorPromptArgs(cfg Config, model, format string, stream bool, prompt string) []string {
	args := []string{"-p", "--trust", "--mode", "ask", "--sandbox", "enabled", "--workspace", cfg.Workspace, "--model", model, "--output-format", format}
	if stream {
		args = append(args, "--stream-partial-output")
	}
	args = append(args, prompt)
	return args
}

func hasUnsupportedTools(raw []byte) bool {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	return containsJSONKey(v, "tools") || containsJSONKey(v, "tool_choice")
}

func containsJSONKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x[key]; ok {
			return true
		}
		for _, vv := range x {
			if containsJSONKey(vv, key) {
				return true
			}
		}
	case []any:
		for _, vv := range x {
			if containsJSONKey(vv, key) {
				return true
			}
		}
	}
	return false
}

func promptFromRequest(format string, raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", statusError("invalid_request", "request body is required", http.StatusBadRequest)
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", statusError("invalid_request", "request body must be JSON", http.StatusBadRequest)
	}
	var b strings.Builder
	b.WriteString("You are running behind CLIProxyAPI through the official Cursor Agent CLI. Answer the user's request directly. Do not modify files or run commands.\n")
	collectPrompt(&b, root, "")
	prompt := strings.TrimSpace(b.String())
	if prompt == "" {
		return "", statusError("invalid_request", "request contains no supported text content", http.StatusBadRequest)
	}
	return prompt, nil
}

func collectPrompt(b *strings.Builder, v any, role string) {
	switch x := v.(type) {
	case map[string]any:
		if t, _ := x["type"].(string); strings.Contains(strings.ToLower(t), "image") || strings.Contains(strings.ToLower(t), "file") {
			return
		}
		if r, _ := x["role"].(string); r != "" {
			role = r
		}
		for _, k := range []string{"instructions", "system", "input", "content", "text"} {
			if vv, ok := x[k]; ok {
				collectPrompt(b, vv, role)
			}
		}
		if msgs, ok := x["messages"]; ok {
			collectPrompt(b, msgs, role)
		}
	case []any:
		for _, item := range x {
			collectPrompt(b, item, role)
		}
	case string:
		text := strings.TrimSpace(x)
		if text == "" {
			return
		}
		if role != "" {
			fmt.Fprintf(b, "\n%s: %s\n", role, text)
		} else {
			fmt.Fprintf(b, "\n%s\n", text)
		}
	}
}

func usageFromCursor(raw map[string]any) map[string]any {
	usage, _ := raw["usage"].(map[string]any)
	if usage == nil {
		return map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}
	in := int64(number(usage["input_tokens"]))
	if in == 0 {
		in = int64(number(usage["prompt_tokens"]))
	}
	if in == 0 {
		in = int64(number(usage["inputTokens"]))
	}
	out := int64(number(usage["output_tokens"]))
	if out == 0 {
		out = int64(number(usage["completion_tokens"]))
	}
	if out == 0 {
		out = int64(number(usage["outputTokens"]))
	}
	total := int64(number(usage["total_tokens"]))
	if total == 0 {
		total = in + out
	}
	return map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": total}
}

func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func cursorText(raw map[string]any) string {
	for _, key := range []string{"result", "text", "output"} {
		if s, _ := raw[key].(string); strings.TrimSpace(s) != "" {
			return s
		}
	}
	if msg, _ := raw["message"].(map[string]any); msg != nil {
		if content, ok := msg["content"].([]any); ok {
			var b strings.Builder
			for _, part := range content {
				m, _ := part.(map[string]any)
				if m == nil {
					continue
				}
				if typ, _ := m["type"].(string); typ != "" && typ != "text" {
					continue
				}
				if s, _ := m["text"].(string); s != "" {
					b.WriteString(s)
				}
			}
			if b.Len() > 0 {
				return b.String()
			}
		}
	}
	return ""
}

func responsePayload(format, model, text string, usage map[string]any, created int64) ([]byte, error) {
	switch format {
	case "claude":
		return json.Marshal(map[string]any{"id": "msg_cursor", "type": "message", "role": "assistant", "model": model, "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": usage})
	case "openai-chat":
		return json.Marshal(map[string]any{"id": "chatcmpl_cursor", "object": "chat.completion", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": usage})
	default:
		return json.Marshal(map[string]any{"id": "resp_cursor", "object": "response", "created_at": created, "model": model, "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}, "usage": usage})
	}
}

func event(payload any) []byte {
	raw, _ := json.Marshal(payload)
	return append(append([]byte("data: "), raw...), '\n', '\n')
}

func (s *Service) emitStreamLine(ctx context.Context, host outputHost, streamID, format, model string, created int64, text string, final bool, usage map[string]any) error {
	var payload any
	if final {
		payload = map[string]any{"type": "cursor.result", "model": model, "text": text, "usage": usage}
	} else if format == "openai-chat" {
		payload = map[string]any{"id": "chatcmpl_cursor", "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}
	} else if format == "claude" {
		payload = map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": text}}
	} else {
		payload = map[string]any{"type": "response.output_text.delta", "delta": text}
	}
	return host.Emit(ctx, streamID, event(payload))
}

func streamCursorJSON(ctx context.Context, host outputHost, streamID, format, model string, created int64, r io.Reader) error {
	dec := json.NewDecoder(r)
	var emitted, terminal string
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	tmpService := &Service{}
	for {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return statusError("cursor_stream_parse", "Cursor stream-json output contained malformed JSON", http.StatusBadGateway)
		}
		typ, _ := line["type"].(string)
		if typ == "result" || line["result"] != nil {
			terminal = cursorText(line)
			usage = usageFromCursor(line)
			continue
		}
		if typ != "assistant" && typ != "text" {
			continue
		}
		text := cursorText(line)
		if text == "" {
			continue
		}
		delta := text
		if strings.HasPrefix(text, emitted) {
			delta = strings.TrimPrefix(text, emitted)
		}
		if delta == "" {
			continue
		}
		emitted += delta
		if err := tmpService.emitStreamLine(ctx, host, streamID, format, model, created, delta, false, nil); err != nil {
			return err
		}
	}
	if terminal == "" {
		terminal = emitted
	}
	return tmpService.emitStreamLine(ctx, host, streamID, format, model, created, terminal, true, usage)
}
