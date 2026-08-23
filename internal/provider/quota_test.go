package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCursorQuotaManagementRouteReturnsSafeObservedUsage(t *testing.T) {
	agent := fakeAgent(t, `
case "$1" in
  models) echo 'auto - Auto';;
  status) printf '{"isAuthenticated":true}\n';;
  about) printf '{"account":{"email":"person@example.test"},"tier":"Pro","version":"2026.08.11"}\n';;
  *) exit 64;;
esac
`)
	s := newTestService(t, agent)
	s.RecordUsage(pluginapi.UsageRecord{
		Provider:    "cursor",
		Model:       "cursor-grok-4.6-high-fast",
		RequestedAt: time.Unix(1700000000, 0).UTC(),
		Detail:      pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
	})

	response, err := s.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/cursor/quota"})
	if err != nil {
		t.Fatalf("quota route: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	var quota CursorQuotaSnapshot
	if err := json.Unmarshal(response.Body, &quota); err != nil {
		t.Fatalf("decode quota: %v", err)
	}
	if quota.Email != "person@example.test" || quota.Tier != "Pro" || !quota.Authenticated {
		t.Fatalf("account metadata=%#v", quota)
	}
	if quota.RemainingQuotaAvailable || quota.Usage.Requests != 1 || quota.Usage.Totals.TotalTokens != 14 {
		t.Fatalf("quota usage=%#v", quota)
	}
}
