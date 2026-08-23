package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementRegistrationIncludesCursorQuotaResourceAndAuthenticatedRoute(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	reg, err := s.RegisterManagement(context.Background(), pluginapi.ManagementRegistrationRequest{ResourceBasePath: "/v0/resource/plugins/cliproxyapi-cursor", BasePath: "/v0/management/plugins/cursor"})
	if err != nil {
		t.Fatalf("RegisterManagement: %v", err)
	}
	quotaRoutes := 0
	for _, route := range reg.Routes {
		if route.Method == http.MethodGet && route.Path == "/plugins/cursor/quota" && route.Menu == "" {
			quotaRoutes++
		}
	}
	if quotaRoutes != 1 {
		t.Fatalf("quota management route not registered exactly once: %#v", reg.Routes)
	}
	quotaResources := 0
	for _, resource := range reg.Resources {
		if resource.Path == "/quota" && resource.Menu == "Cursor Quota" && strings.Contains(resource.Description, "account") {
			quotaResources++
		}
	}
	if quotaResources != 1 {
		t.Fatalf("quota resource not registered exactly once: %#v", reg.Resources)
	}
}

func TestQuotaResourceIsStaticUnauthenticatedAndContainsNoAccountData(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `if [ "$1" = "about" ]; then echo '{"account":"alice@example.test"}'; exit 0; fi
exit 0`))
	resp, err := s.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/cliproxyapi-cursor/quota"})
	if err != nil {
		t.Fatalf("HandleManagement: %v", err)
	}
	body := string(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Cursor Quota") {
		t.Fatalf("quota resource response=%d %s", resp.StatusCode, body)
	}
	for _, forbidden := range []string{"alice@example.test", "secret", "remaining_tokens"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("static quota page leaked account/quota data %q in %s", forbidden, body)
		}
	}
	if strings.Contains(body, "localStorage") || strings.Contains(body, "sessionStorage") {
		t.Fatalf("management key must stay in page memory only: %s", body)
	}
	if !strings.Contains(body, "/v0/management/plugins/cursor/quota") || !strings.Contains(strings.ToLower(body), "management key") {
		t.Fatalf("quota page does not instruct authenticated fetch: %s", body)
	}
}

func TestQuotaEndpointUsesAgentAboutAndRedactsUnsafeFields(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "about" ]; then
  printf '%s\n' '{"userEmail":"alice@example.test","subscriptionTier":"Pro","cliVersion":"2026.08.11-e8db854","account":{"id":"secret-account-id"},"accessToken":"secret-token","refresh_token":"secret-refresh"}'
  exit 0
fi
exit 64
`)
	s := newTestService(t, agent)
	resp, err := s.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/cursor/quota"})
	if err != nil {
		t.Fatalf("HandleManagement: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quota response=%d %s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode quota json: %v: %s", err, resp.Body)
	}
	if body["account"] != "alice@example.test" || body["tier"] != "Pro" || body["version"] != "2026.08.11-e8db854" {
		t.Fatalf("safe fields not parsed: %#v", body)
	}
	remaining, _ := body["remaining_quota"].(map[string]any)
	if remaining["available"] != false || !strings.Contains(remaining["reason"].(string), "agent about") {
		t.Fatalf("remaining quota not explicitly unavailable: %#v", body)
	}
	for _, forbidden := range []string{"secret-token", "secret-refresh", "secret-account-id", "accessToken", "refresh_token"} {
		if strings.Contains(string(resp.Body), forbidden) {
			t.Fatalf("quota JSON leaked forbidden field/value %q: %s", forbidden, resp.Body)
		}
	}
}

func TestQuotaEndpointRejectsMalformedAgentAboutJSON(t *testing.T) {
	agent := fakeAgent(t, `if [ "$1" = "about" ]; then printf '{not-json'; exit 0; fi
exit 64`)
	s := newTestService(t, agent)
	resp, err := s.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/plugins/cursor/quota"})
	if err != nil {
		t.Fatalf("HandleManagement: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(resp.Body), "malformed") {
		t.Fatalf("malformed about response=%d %s", resp.StatusCode, resp.Body)
	}
}
