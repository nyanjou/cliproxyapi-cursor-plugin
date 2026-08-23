package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostDecodedManagementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

func TestManagementRegisterRPCEnvelopeDecodesLikeExternalHost(t *testing.T) {
	req, err := json.Marshal(pluginapi.ManagementRegistrationRequest{
		BasePath:         "/v0/management/plugins/cursor",
		ResourceBasePath: "/v0/resource/plugins/cliproxyapi-cursor",
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, failed := handleMethod(pluginabi.MethodManagementRegister, req)
	if failed {
		t.Fatalf("management.register failed: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"Handler"`)) || bytes.Contains(raw, []byte(`"handler"`)) {
		t.Fatalf("management.register envelope leaked Handler field: %s", raw)
	}

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v: %s", err, raw)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("unexpected envelope: %#v body=%s", envelope, raw)
	}
	var resp hostDecodedManagementRegistrationResponse
	if err := json.Unmarshal(envelope.Result, &resp); err != nil {
		t.Fatalf("decode plugin result management.register: %v: result=%s", err, envelope.Result)
	}
	if len(resp.Routes) != 3 {
		t.Fatalf("routes=%#v", resp.Routes)
	}
	wantRoutes := map[string]string{
		http.MethodGet + " /plugins/cursor/quota":          "Reports safe Cursor account metadata and usage observed by CLIProxyAPI.",
		http.MethodGet + " /plugins/cursor/setup/status":   "Reports managed official Cursor Agent CLI installation status.",
		http.MethodPost + " /plugins/cursor/setup/install": "Explicitly installs the official Cursor Agent CLI into the plugin runtime HOME.",
	}
	for _, route := range resp.Routes {
		key := route.Method + " " + route.Path
		if wantRoutes[key] == "" || route.Description != wantRoutes[key] {
			t.Fatalf("unexpected route: %#v all=%#v", route, resp.Routes)
		}
		if route.Handler != nil {
			t.Fatalf("host-injected handler should be nil before adapter injection: %#v", route.Handler)
		}
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("resources=%#v", resp.Resources)
	}
	foundSetup := false
	for _, resource := range resp.Resources {
		if resource.Path == "/setup" && resource.Menu == "Cursor Agent setup" && strings.Contains(resource.Description, "official Cursor Agent CLI") {
			foundSetup = true
		}
		if resource.Handler != nil {
			t.Fatalf("host-injected resource handler should be nil before adapter injection: %#v", resource.Handler)
		}
	}
	if !foundSetup {
		t.Fatalf("unexpected resources: %#v", resp.Resources)
	}
}

func TestPluginRegisterIncludesLogoAndUsageCapability(t *testing.T) {
	raw, failed := handleMethod(pluginabi.MethodPluginRegister, nil)
	if failed {
		t.Fatalf("plugin.register failed: %s", raw)
	}

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v: %s", err, raw)
	}
	var reg registration
	if err := json.Unmarshal(envelope.Result, &reg); err != nil {
		t.Fatalf("decode registration: %v: result=%s", err, envelope.Result)
	}
	if !strings.HasPrefix(reg.Metadata.Logo, "data:image/svg+xml;base64,") {
		t.Fatalf("missing inline Cursor logo: %q", reg.Metadata.Logo)
	}
	if !reg.Capabilities.UsagePlugin {
		t.Fatalf("usage plugin capability was not registered: %#v", reg.Capabilities)
	}
}

func TestUsageHandleRecordsCursorUsage(t *testing.T) {
	pluginService.Shutdown()
	t.Cleanup(pluginService.Shutdown)
	record := pluginapi.UsageRecord{
		Provider:    "cursor",
		Model:       "cursor/auto",
		RequestedAt: time.Unix(1700000060, 0).UTC(),
		Detail:      pluginapi.UsageDetail{InputTokens: 11, OutputTokens: 7, ReasoningTokens: 3, TotalTokens: 21},
	}
	rawRecord, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if raw, failed := handleMethod(pluginabi.MethodUsageHandle, rawRecord); failed {
		t.Fatalf("usage.handle failed: %s", raw)
	}

	usage := pluginService.GatewayUsage()
	if usage.Requests != 1 {
		t.Fatalf("usage requests not recorded: %#v", usage)
	}
	if usage.Totals.TotalTokens != 21 {
		t.Fatalf("usage totals not recorded: %#v", usage)
	}
}
