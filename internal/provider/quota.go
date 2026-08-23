package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type CursorQuota struct {
	Account                    string         `json:"account,omitempty"`
	Tier                       string         `json:"tier,omitempty"`
	Version                    string         `json:"version,omitempty"`
	RemainingQuota             quotaRemaining `json:"remaining_quota"`
	Source                     string         `json:"source"`
	ManagerPlusQuotaLimitation string         `json:"manager_plus_quota_limitation"`
}

type quotaRemaining struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

func (s *Service) CursorQuota(ctx context.Context) (CursorQuota, error) {
	cfg := s.Config()
	result, err := s.runAgent(ctx, cfg, []string{"about", "--format", "json"}, nil, false)
	if err != nil {
		return CursorQuota{}, err
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(result.Stdout))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return CursorQuota{}, fmt.Errorf("Cursor agent about JSON was malformed")
	}
	return quotaFromAbout(raw), nil
}

func quotaFromAbout(raw map[string]any) CursorQuota {
	q := CursorQuota{
		Account:                    firstSafeString(raw, "userEmail", "user_email", "email"),
		Tier:                       firstSafeString(raw, "tier", "plan", "subscriptionTier", "subscription_tier"),
		Version:                    firstSafeString(raw, "version", "cliVersion", "cli_version", "agentVersion", "agent_version", "cursorVersion", "cursor_version"),
		RemainingQuota:             quotaRemaining{Available: false, Reason: "The official Cursor `agent about --format json` output does not expose numeric remaining subscription quota."},
		Source:                     "official Cursor CLI: agent about --format json",
		ManagerPlusQuotaLimitation: "Stock CPA Manager Plus has a hard-coded Quota page; CLIProxyAPI plugin ABI v7.2.138 cannot inject a section there, so this plugin exposes Cursor Quota as a native plugin management resource.",
	}
	if q.Account == "" {
		q.Account = safeAccount(raw["account"])
	}
	return q
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

func quotaHTML() string {
	return `<!doctype html><meta charset="utf-8"><title>Cursor Quota</title><main><h1>Cursor Quota</h1><p>This static plugin resource contains no Cursor account data. Enter the CLIProxyAPI management key to fetch safe account, tier, and CLI version fields from the authenticated plugin management endpoint.</p><p>Numeric remaining Cursor subscription quota is unavailable because the official Cursor CLI boundary (<code>agent about --format json</code>) does not expose it. This plugin will not fabricate quota and does not read Cursor OAuth files or private Cursor endpoints.</p><p>CPA Manager Plus has a hard-coded Quota page; plugin ABI v7.2.138 cannot inject a custom section there, so this native plugin management resource is the supported Cursor Quota page.</p><label>Management key <input id="k" type="password" autocomplete="off"></label><button id="load">Load Cursor Quota</button><pre id="out"></pre><script>const input=document.getElementById('k');document.getElementById('load').onclick=async()=>{const r=await fetch('/v0/management/plugins/cursor/quota',{headers:{'authorization':'Bearer '+input.value}});document.getElementById('out').textContent=await r.text();};</script></main>`
}
