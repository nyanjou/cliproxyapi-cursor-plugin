package provider

import (
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

type BufferedStreamResponse struct {
	Headers http.Header
	Chunks  []pluginapi.ExecutorStreamChunk
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
	result, err := s.runAgent(ctx, cfg, cursorPromptArgs(cfg, model, prompt), nil, false)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	parsed, err := parseCursorJSONResult(result)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	text := cursorText(parsed)
	payload, err := responsePayload(format, req.Model, text, usageFromCursor(parsed), s.now().Unix())
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: jsonHeaders(), Metadata: map[string]any{"cursor_cli": true, "cursor_model": model, "harness": "official-agent-cli-ask-sandbox-disabled"}}, nil
}

func (s *Service) ExecuteStream(ctx context.Context, req ExecuteRequest) (BufferedStreamResponse, error) {
	if strings.TrimSpace(req.StreamID) == "" {
		return BufferedStreamResponse{}, statusError("invalid_request", "stream_id is required", http.StatusBadRequest)
	}
	inputFormat := normalizeRequestFormat(firstNonEmpty(req.SourceFormat, req.Format))
	if inputFormat == "" {
		return BufferedStreamResponse{}, statusError("unsupported_format", fmt.Sprintf("unsupported request format %q", firstNonEmpty(req.SourceFormat, req.Format)), http.StatusUnprocessableEntity)
	}
	outputFormat := normalizeRequestFormat(firstNonEmpty(req.Format, req.SourceFormat))
	if outputFormat == "" {
		return BufferedStreamResponse{}, statusError("unsupported_format", fmt.Sprintf("unsupported response format %q", firstNonEmpty(req.Format, req.SourceFormat)), http.StatusUnprocessableEntity)
	}
	raw := firstPayload(req)
	if err := validateRequestPayload(raw); err != nil {
		return BufferedStreamResponse{}, err
	}
	model, err := s.resolveModel(ctx, req.Model)
	if err != nil {
		return BufferedStreamResponse{}, err
	}
	prompt, err := promptFromRequest(inputFormat, raw)
	if err != nil {
		return BufferedStreamResponse{}, err
	}
	cfg := s.Config()
	if len(raw) > cfg.MaxRequestBytes {
		return BufferedStreamResponse{}, statusError("request_too_large", "request body exceeds max_request_bytes", http.StatusRequestEntityTooLarge)
	}
	if len([]byte(prompt)) > cfg.MaxPromptBytes {
		return BufferedStreamResponse{}, statusError("prompt_too_large", "encoded Cursor prompt exceeds max_prompt_bytes", http.StatusRequestEntityTooLarge)
	}
	result, err := s.runAgent(ctx, cfg, cursorPromptArgs(cfg, model, prompt), nil, false)
	if err != nil {
		return BufferedStreamResponse{}, err
	}
	parsed, err := parseCursorJSONResult(result)
	if err != nil {
		return BufferedStreamResponse{}, err
	}
	chunks, err := bufferedStreamChunks(outputFormat, req.Model, cursorText(parsed), usageFromCursor(parsed), s.now().Unix())
	if err != nil {
		return BufferedStreamResponse{}, err
	}
	headers := http.Header{}
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	return BufferedStreamResponse{Headers: headers, Chunks: chunks}, nil
}

func bufferedStreamChunks(format, model, text string, usage map[string]any, created int64) ([]pluginapi.ExecutorStreamChunk, error) {
	frames, err := streamFrames(format, model, text, usage, created)
	if err != nil {
		return nil, err
	}
	chunks := make([]pluginapi.ExecutorStreamChunk, 0, len(frames))
	for _, frame := range frames {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: frame})
	}
	return chunks, nil
}

func streamFrames(format, model, text string, usage map[string]any, created int64) ([][]byte, error) {
	switch format {
	case "openai-chat":
		return openAIChatStreamFrames(model, text, usage, created)
	case "claude":
		return claudeStreamFrames(model, text, usage)
	case "openai-response":
		return openAIResponseStreamFrames(model, text, usage, created)
	default:
		return nil, statusError("unsupported_format", fmt.Sprintf("unsupported response format %q", format), http.StatusUnprocessableEntity)
	}
}

func sseFrame(eventName string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if eventName == "" {
		return append(append([]byte("data: "), raw...), []byte("\n\n")...), nil
	}
	frame := append([]byte("event: "+eventName+"\ndata: "), raw...)
	return append(frame, []byte("\n\n")...), nil
}

func openAIChatStreamFrames(model, text string, usage map[string]any, created int64) ([][]byte, error) {
	id := "chatcmpl_cursor"
	first, err := sseFrame("", map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}}})
	if err != nil {
		return nil, err
	}
	final, err := sseFrame("", map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": usage})
	if err != nil {
		return nil, err
	}
	return [][]byte{first, final, []byte("data: [DONE]\n\n")}, nil
}

func claudeStreamFrames(model, text string, usage map[string]any) ([][]byte, error) {
	events := []struct {
		name    string
		payload any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_cursor", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": usage["input_tokens"], "output_tokens": 0}}}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}},
		{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}},
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": usage["output_tokens"]}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	}
	frames := make([][]byte, 0, len(events))
	for _, item := range events {
		frame, err := sseFrame(item.name, item.payload)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func openAIResponseStreamFrames(model, text string, usage map[string]any, created int64) ([][]byte, error) {
	responseID := "resp_cursor"
	messageID := "msg_cursor"
	response := map[string]any{"id": responseID, "object": "response", "created_at": created, "status": "completed", "model": model, "output": []any{map[string]any{"id": messageID, "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}, "usage": usage}
	events := []struct {
		name    string
		payload any
	}{
		{"response.created", map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{"id": responseID, "object": "response", "created_at": created, "status": "in_progress", "model": model, "output": []any{}}}},
		{"response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": map[string]any{"id": messageID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}},
		{"response.content_part.added", map[string]any{"type": "response.content_part.added", "sequence_number": 2, "item_id": messageID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}},
		{"response.output_text.delta", map[string]any{"type": "response.output_text.delta", "sequence_number": 3, "item_id": messageID, "output_index": 0, "content_index": 0, "delta": text, "logprobs": []any{}}},
		{"response.output_text.done", map[string]any{"type": "response.output_text.done", "sequence_number": 4, "item_id": messageID, "output_index": 0, "content_index": 0, "text": text, "logprobs": []any{}}},
		{"response.content_part.done", map[string]any{"type": "response.content_part.done", "sequence_number": 5, "item_id": messageID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}},
		{"response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": 6, "output_index": 0, "item": map[string]any{"id": messageID, "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}},
		{"response.completed", map[string]any{"type": "response.completed", "sequence_number": 7, "response": response}},
	}
	frames := make([][]byte, 0, len(events))
	for _, item := range events {
		frame, err := sseFrame(item.name, item.payload)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
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
