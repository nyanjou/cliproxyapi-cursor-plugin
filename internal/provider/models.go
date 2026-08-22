package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type cursorModel struct{ ID, Name string }

func (s *Service) StaticModels() pluginapi.ModelResponse {
	return pluginapi.ModelResponse{Provider: providerID, Models: []pluginapi.ModelInfo{}}
}

func (s *Service) ModelsForAuth(ctx context.Context, _ string, _ pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	models, err := s.discoverModels(ctx)
	if err != nil {
		return pluginapi.ModelResponse{}, err
	}
	return pluginapi.ModelResponse{Provider: providerID, Models: modelInfos(models, s.Config().ModelPrefix)}, nil
}

func (s *Service) discoverModels(ctx context.Context) ([]cursorModel, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return nil, statusError("plugin_disabled", "Cursor plugin is disabled", http.StatusServiceUnavailable)
	}
	now := s.now()
	s.modelMu.Lock()
	if cfg.ModelCacheTTLSeconds > 0 && len(s.modelsCache) > 0 && s.modelExpires.After(now) {
		cached := append([]cursorModel(nil), s.modelsCache...)
		s.modelMu.Unlock()
		return cached, nil
	}
	s.modelMu.Unlock()
	result, err := s.runAgent(ctx, cfg, []string{"models"}, nil, false)
	if err != nil {
		return nil, fmt.Errorf("discover Cursor models: %w", err)
	}
	models := filterCursorModels(parseModelLines(string(result.Stdout)), cfg)
	if len(models) == 0 {
		return nil, statusError("models_empty", "Cursor agent models returned no usable models", http.StatusBadGateway)
	}
	s.modelMu.Lock()
	s.modelsCache = append([]cursorModel(nil), models...)
	s.modelExpires = now.Add(cfg.modelCacheTTL())
	s.modelMu.Unlock()
	return models, nil
}

func parseModelLines(out string) []cursorModel {
	seen := map[string]struct{}{}
	models := []cursorModel{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "available models") {
			continue
		}
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		if id == "" || strings.ContainsAny(id, " \t/") {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, cursorModel{ID: id, Name: name})
	}
	sort.SliceStable(models, func(i, j int) bool { return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID) })
	return models
}

func filterCursorModels(models []cursorModel, cfg Config) []cursorModel {
	allowed := map[string]struct{}{}
	for _, m := range cfg.AllowedModels {
		allowed[strings.ToLower(strings.TrimPrefix(m, cfg.ModelPrefix))] = struct{}{}
	}
	denied := map[string]struct{}{}
	for _, m := range cfg.DeniedModels {
		denied[strings.ToLower(strings.TrimPrefix(m, cfg.ModelPrefix))] = struct{}{}
	}
	out := []cursorModel{}
	for _, model := range models {
		idLower := strings.ToLower(model.ID)
		if len(allowed) > 0 {
			if _, ok := allowed[idLower]; !ok {
				continue
			}
		}
		if _, ok := denied[idLower]; ok {
			continue
		}
		excluded := false
		for _, p := range cfg.ExcludedModelPrefixes {
			if strings.HasPrefix(idLower, strings.ToLower(p)) {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, model)
		}
	}
	return out
}

func modelInfos(models []cursorModel, prefix string) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, model := range models {
		id := prefix + model.ID
		name := model.Name
		if name == "" {
			name = model.ID
		}
		out = append(out, pluginapi.ModelInfo{ID: id, Object: "model", OwnedBy: "cursor", Type: "chat", DisplayName: name, Name: model.ID, Description: "Experimental Cursor Agent CLI-backed model; runs through official agent CLI in read-only ask mode", SupportedGenerationMethods: []string{"openai-response", "openai-chat", "claude"}, SupportedParameters: []string{"stream"}, SupportedInputModalities: []string{"TEXT"}, SupportedOutputModalities: []string{"TEXT"}, Created: time.Now().Unix()})
	}
	return out
}

func (s *Service) resolveModel(ctx context.Context, requested string) (string, error) {
	cfg := s.Config()
	model := strings.TrimSpace(strings.TrimPrefix(requested, cfg.ModelPrefix))
	if model == "" {
		return "", statusError("invalid_request", "model is required", http.StatusBadRequest)
	}
	models, err := s.discoverModels(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range models {
		if strings.EqualFold(m.ID, model) {
			return m.ID, nil
		}
	}
	return "", statusError("model_not_found", "Cursor model is not present in `agent models` catalog", http.StatusNotFound)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
