package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type ExecuteRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type HTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (pluginapi.ExecutorResponse, error) {
	format := normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format))
	if format == "" {
		return pluginapi.ExecutorResponse{}, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	raw := firstPayload(req)
	if err := validateRequestPayload(raw); err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	model, err := s.resolveModel(ctx, req.Model)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	prompt, err := promptFromRequest(format, raw)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	cfg := s.Config()
	if len(raw) > cfg.MaxRequestBytes {
		return pluginapi.ExecutorResponse{}, statusError("request_too_large", "request body exceeds max_request_bytes", http.StatusRequestEntityTooLarge)
	}
	if len([]byte(prompt)) > cfg.MaxPromptBytes {
		return pluginapi.ExecutorResponse{}, statusError("prompt_too_large", "encoded Cursor prompt exceeds max_prompt_bytes", http.StatusRequestEntityTooLarge)
	}
	result, err := s.runAgent(ctx, cfg, cursorPromptArgs(cfg, model, "json", false, prompt), nil, false)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	var parsed map[string]any
	dec := json.NewDecoder(bytes.NewReader(result.Stdout))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return pluginapi.ExecutorResponse{}, statusError("cursor_json_parse", "Cursor agent JSON output was malformed", http.StatusBadGateway)
	}
	text := cursorText(parsed)
	if text == "" {
		return pluginapi.ExecutorResponse{}, statusError("cursor_empty_result", "Cursor agent JSON output contained no result text", http.StatusBadGateway)
	}
	payload, err := responsePayload(format, req.Model, text, usageFromCursor(parsed), s.now().Unix())
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: jsonHeaders(), Metadata: map[string]any{"cursor_cli": true, "cursor_model": model, "harness": "official-agent-cli-ask-sandbox"}}, nil
}

func (s *Service) ExecuteStream(ctx context.Context, req ExecuteRequest) (http.Header, error) {
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, statusError("invalid_request", "stream_id is required", http.StatusBadRequest)
	}
	format := normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format))
	if format == "" {
		return nil, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	raw := firstPayload(req)
	if err := validateRequestPayload(raw); err != nil {
		return nil, err
	}
	model, err := s.resolveModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	prompt, err := promptFromRequest(format, raw)
	if err != nil {
		return nil, err
	}
	cfg := s.Config()
	if len(raw) > cfg.MaxRequestBytes {
		return nil, statusError("request_too_large", "request body exceeds max_request_bytes", http.StatusRequestEntityTooLarge)
	}
	if len([]byte(prompt)) > cfg.MaxPromptBytes {
		return nil, statusError("prompt_too_large", "encoded Cursor prompt exceeds max_prompt_bytes", http.StatusRequestEntityTooLarge)
	}
	host, ok := s.host.(outputHost)
	if !ok {
		return nil, statusError("stream_unavailable", "host does not implement output streaming callbacks", http.StatusInternalServerError)
	}
	go func() {
		message := ""
		defer func() { host.CloseOutput(context.Background(), req.StreamID, message) }()
		result, err := s.runAgent(ctx, cfg, cursorPromptArgs(cfg, model, "stream-json", true, prompt), nil, false)
		if err != nil {
			message = err.Error()
			return
		}
		if err := streamCursorJSON(context.Background(), host, req.StreamID, format, req.Model, s.now().Unix(), bytes.NewReader(result.Stdout)); err != nil {
			message = err.Error()
		}
	}()
	headers := http.Header{}
	headers.Set("Content-Type", "application/x-ndjson")
	headers.Set("Cache-Control", "no-cache")
	return headers, nil
}

func (s *Service) HTTP(context.Context, HTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, statusError("unsupported_http", "Cursor provider does not proxy raw HTTP or private Cursor endpoints", http.StatusForbidden)
}

func normalizeRequestFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "responses", "openai-response", "openai-responses":
		return "openai-response"
	case "chat", "openai-chat", "chat-completions":
		return "openai-chat"
	case "claude", "anthropic", "anthropic-messages":
		return "claude"
	default:
		return ""
	}
}

func firstPayload(req ExecuteRequest) []byte {
	if len(req.OriginalRequest) > 0 {
		return req.OriginalRequest
	}
	return req.Payload
}

func jsonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return h
}
