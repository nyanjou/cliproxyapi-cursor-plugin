package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultAgentPath = "agent"

type Config struct {
	Enabled               bool     `yaml:"enabled" json:"enabled"`
	ExecutablePath        string   `yaml:"executable_path" json:"executable_path"`
	Workspace             string   `yaml:"workspace" json:"workspace"`
	ModelPrefix           string   `yaml:"model_prefix" json:"model_prefix"`
	AllowedModels         []string `yaml:"allowed_models" json:"allowed_models"`
	DeniedModels          []string `yaml:"denied_models" json:"denied_models"`
	ExcludedModelPrefixes []string `yaml:"excluded_model_prefixes" json:"excluded_model_prefixes"`
	EnvironmentAllowlist  []string `yaml:"environment_allowlist" json:"environment_allowlist"`
	TimeoutSeconds        int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	ModelCacheTTLSeconds  int      `yaml:"model_cache_ttl_seconds" json:"model_cache_ttl_seconds"`
	MaxPromptBytes        int      `yaml:"max_prompt_bytes" json:"max_prompt_bytes"`
	MaxRequestBytes       int      `yaml:"max_request_bytes" json:"max_request_bytes"`
	MaxStdoutBytes        int      `yaml:"max_stdout_bytes" json:"max_stdout_bytes"`
	MaxStderrBytes        int      `yaml:"max_stderr_bytes" json:"max_stderr_bytes"`
	MaxConcurrent         int      `yaml:"max_concurrent" json:"max_concurrent"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:              true,
		ExecutablePath:       defaultAgentPath,
		Workspace:            filepath.Join(os.TempDir(), "cliproxyapi-cursor-workspace"),
		TimeoutSeconds:       120,
		ModelCacheTTLSeconds: 600,
		MaxPromptBytes:       512 * 1024,
		MaxRequestBytes:      1024 * 1024,
		MaxStdoutBytes:       2 * 1024 * 1024,
		MaxStderrBytes:       64 * 1024,
		MaxConcurrent:        1,
		EnvironmentAllowlist: []string{"HOME", "PATH", "SHELL", "USER", "LOGNAME", "TMPDIR", "NO_COLOR", "TERM"},
	}
}

func ParseConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", err)
		}
	}
	cfg.ExecutablePath = strings.TrimSpace(cfg.ExecutablePath)
	if cfg.ExecutablePath == "" {
		return Config{}, fmt.Errorf("executable_path is required")
	}
	if strings.Contains(cfg.ExecutablePath, "\x00") {
		return Config{}, fmt.Errorf("executable_path contains NUL")
	}
	cfg.Workspace = strings.TrimSpace(cfg.Workspace)
	if cfg.Workspace == "" {
		return Config{}, fmt.Errorf("workspace is required")
	}
	if !filepath.IsAbs(cfg.Workspace) {
		return Config{}, fmt.Errorf("workspace must be an absolute path")
	}
	cfg.ModelPrefix = strings.TrimSpace(cfg.ModelPrefix)
	cfg.AllowedModels = normalizeStringSet(cfg.AllowedModels)
	cfg.DeniedModels = normalizeStringSet(cfg.DeniedModels)
	cfg.ExcludedModelPrefixes = normalizeModelPrefixes(cfg.ExcludedModelPrefixes)
	cfg.EnvironmentAllowlist = normalizeEnvAllowlist(cfg.EnvironmentAllowlist)
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 1800 {
		return Config{}, fmt.Errorf("timeout_seconds must be between 1 and 1800")
	}
	if cfg.ModelCacheTTLSeconds < 0 || cfg.ModelCacheTTLSeconds > 3600 {
		return Config{}, fmt.Errorf("model_cache_ttl_seconds must be between 0 and 3600")
	}
	if cfg.MaxPromptBytes < 1024 || cfg.MaxPromptBytes > 8*1024*1024 {
		return Config{}, fmt.Errorf("max_prompt_bytes must be between 1024 and 8388608")
	}
	if cfg.MaxRequestBytes < 1024 || cfg.MaxRequestBytes > 16*1024*1024 {
		return Config{}, fmt.Errorf("max_request_bytes must be between 1024 and 16777216")
	}
	if cfg.MaxStdoutBytes < 1024 || cfg.MaxStdoutBytes > 16*1024*1024 {
		return Config{}, fmt.Errorf("max_stdout_bytes must be between 1024 and 16777216")
	}
	if cfg.MaxStderrBytes < 1024 || cfg.MaxStderrBytes > 1024*1024 {
		return Config{}, fmt.Errorf("max_stderr_bytes must be between 1024 and 1048576")
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 8 {
		return Config{}, fmt.Errorf("max_concurrent must be between 1 and 8")
	}
	return cfg, nil
}

func (c Config) timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }
func (c Config) modelCacheTTL() time.Duration {
	return time.Duration(c.ModelCacheTTLSeconds) * time.Second
}

func normalizeModelPrefixes(prefixes []string) []string {
	out := normalizeStringSet(prefixes)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return dedupeStrings(out)
}
func normalizeStringSet(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return dedupeStrings(out)
}
func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return out
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func normalizeEnvAllowlist(values []string) []string {
	out := []string{}
	for _, v := range normalizeStringSet(values) {
		if envNameRE.MatchString(v) {
			out = append(out, v)
		}
	}
	return out
}
