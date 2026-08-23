package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseAuthReturnsNativeQuotaFriendlyAuths(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	resp, err := s.ParseAuth(pluginapi.AuthParseRequest{
		Provider: "cursor",
		FileName: "cursor-cli.json",
		RawJSON:  []byte(`{"type":"cursor","email":"alice@example.test","tier":"Pro","version":"smoke-cli-1","authenticated":true,"status_known":true}`),
	})
	if err != nil {
		t.Fatalf("ParseAuth: %v", err)
	}
	if !resp.Handled || len(resp.Auths) != 1 {
		t.Fatalf("parse response = %#v", resp)
	}
	auth := resp.Auths[0]
	if resp.Auth.ID != auth.ID {
		t.Fatalf("compat Auth was not populated: %#v", resp)
	}
	if auth.Provider != "cursor" || auth.ID != "cursor-cli" || auth.FileName != "cursor-cli.json" {
		t.Fatalf("auth identity = %#v", auth)
	}
	if auth.Label != "alice@example.test" {
		t.Fatalf("label = %q", auth.Label)
	}
	if auth.Metadata["email"] != "alice@example.test" || auth.Metadata["type"] != "cursor" {
		t.Fatalf("native auth metadata = %#v", auth.Metadata)
	}
	if auth.Attributes["auth_kind"] != "oauth" || auth.Attributes["account_email"] != "alice@example.test" {
		t.Fatalf("native auth attributes = %#v", auth.Attributes)
	}
}

func TestStatusStorageEnrichesSafeAccountFieldsFromAbout(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "status" ]; then echo '{"authenticated":true}'; exit 0; fi
if [ "$1" = "about" ] && [ "$2" = "--format" ] && [ "$3" = "json" ]; then
  printf '%s\n' '{"userEmail":"alice@example.test","subscriptionTier":"Pro","cliVersion":"smoke-cli-1","account":{"id":"secret-account-id"}}'
  exit 0
fi
exit 64
`)
	s := newTestService(t, agent)
	st, err := s.statusStorage(context.Background())
	if err != nil {
		t.Fatalf("statusStorage: %v", err)
	}
	if !st.StatusKnown || !st.Authenticated {
		t.Fatalf("status not parsed: %#v", st)
	}
	if st.Email != "alice@example.test" || st.Tier != "Pro" || st.Version != "smoke-cli-1" {
		t.Fatalf("about fields not parsed: %#v", st)
	}
	auth, err := s.authData(st, "")
	if err != nil {
		t.Fatalf("authData: %v", err)
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-account-id", "accessToken", "refresh_token"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("auth data leaked forbidden field/value %q: %s", forbidden, raw)
		}
	}
}

func TestStaticAndPerAuthModelsProvideCursorAutoFallback(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 64`))
	cfg := s.Config()
	cfg.ModelPrefix = "cursor/"
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}

	static := s.StaticModels()
	if len(static.Models) != 1 || static.Models[0].ID != "cursor/auto" {
		t.Fatalf("static models = %#v", static.Models)
	}
	perAuth, err := s.ModelsForAuth(context.Background(), "", pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatalf("ModelsForAuth should use fallback, got %v", err)
	}
	if len(perAuth.Models) != 1 || perAuth.Models[0].ID != "cursor/auto" {
		t.Fatalf("per-auth models = %#v", perAuth.Models)
	}
}
