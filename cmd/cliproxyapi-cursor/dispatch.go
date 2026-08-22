package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nyanjou/cliproxyapi-cursor-plugin/internal/provider"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginService = provider.New(hostTransport{})
var pluginVersion = "0.2.0"

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	ManagementAPI         bool                         `json:"management_api"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRegistrationResponse struct {
	Routes    []rpcManagementRoute `json:"routes,omitempty"`
	Resources []rpcResourceRoute   `json:"resources,omitempty"`
}

type rpcManagementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type rpcResourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

func handleMethod(method string, request []byte) ([]byte, bool) {
	result, errHandle := dispatch(method, request)
	if errHandle != nil {
		var statusErr *provider.StatusError
		if errors.As(errHandle, &statusErr) {
			return errorEnvelope(statusErr.Code, statusErr.Message, statusErr.HTTPStatus, statusErr.Retryable), true
		}
		return errorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError, false), true
	}
	raw, errEnvelope := okEnvelope(result)
	if errEnvelope != nil {
		return errorEnvelope("encoding_error", errEnvelope.Error(), http.StatusInternalServerError, false), true
	}
	return raw, false
}

func dispatch(method string, request []byte) (any, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
		}
		if errConfigure := pluginService.Configure(req.ConfigYAML); errConfigure != nil {
			return nil, errConfigure
		}
		return pluginRegistration(), nil
	case pluginabi.MethodModelStatic:
		return pluginService.StaticModels(), nil
	case pluginabi.MethodModelForAuth:
		var req rpcAuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ModelsForAuth(ctx, req.HostCallbackID, req.AuthModelRequest)
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return identifierResponse{Identifier: "cursor"}, nil
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.ParseAuth(req)
	case pluginabi.MethodAuthLoginStart:
		var req rpcAuthLoginStartRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.StartLogin(ctx, req.HostCallbackID, req.AuthLoginStartRequest)
	case pluginabi.MethodAuthLoginPoll:
		var req rpcAuthLoginPollRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.PollLogin(ctx, req.HostCallbackID, req.State)
	case pluginabi.MethodAuthRefresh:
		var req rpcAuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.RefreshAuth(ctx, req.HostCallbackID, req.AuthRefreshRequest)
	case pluginabi.MethodExecutorExecute:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.Execute(ctx, req)
	case pluginabi.MethodExecutorExecuteStream:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		headers, errStream := pluginService.ExecuteStream(ctx, req)
		if errStream != nil {
			return nil, errStream
		}
		return map[string]any{"headers": headers}, nil
	case pluginabi.MethodExecutorCountTokens:
		var req provider.ExecuteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.CountTokens(req)
	case pluginabi.MethodExecutorHTTPRequest:
		var req provider.HTTPRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.HTTP(ctx, req)
	case pluginabi.MethodManagementRegister:
		var req pluginapi.ManagementRegistrationRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		reg, errRegister := pluginService.RegisterManagement(ctx, req)
		if errRegister != nil {
			return nil, errRegister
		}
		return rpcManagementRegistrationFromProvider(reg), nil
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return pluginService.HandleManagement(ctx, req)
	default:
		return nil, &provider.StatusError{
			Code:       "unknown_method",
			Message:    "unknown plugin method: " + method,
			HTTPStatus: http.StatusNotImplemented,
		}
	}
}

func rpcManagementRegistrationFromProvider(reg pluginapi.ManagementRegistrationResponse) rpcManagementRegistrationResponse {
	routes := make([]rpcManagementRoute, 0, len(reg.Routes))
	for _, route := range reg.Routes {
		routes = append(routes, rpcManagementRoute{
			Method:      route.Method,
			Path:        route.Path,
			Menu:        route.Menu,
			Description: route.Description,
		})
	}
	resources := make([]rpcResourceRoute, 0, len(reg.Resources))
	for _, resource := range reg.Resources {
		resources = append(resources, rpcResourceRoute{
			Path:        resource.Path,
			Menu:        resource.Menu,
			Description: resource.Description,
		})
	}
	return rpcManagementRegistrationResponse{Routes: routes, Resources: resources}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Cursor Agent CLI provider",
			Version:          pluginVersion,
			Author:           "Nyanjou; based on MIT-licensed arthur-sommer-etc cliproxyapi-copilot-plugin ABI scaffolding",
			GitHubRepository: "https://github.com/nyanjou/cliproxyapi-cursor-plugin",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable the Cursor Agent CLI-backed provider."},
				{Name: "executable_path", Type: pluginapi.ConfigFieldTypeString, Description: "Path to the official Cursor Agent CLI executable (agent)."},
				{Name: "workspace", Type: pluginapi.ConfigFieldTypeString, Description: "Dedicated empty workspace used for all read-only ask-mode CLI runs."},
				{Name: "model_prefix", Type: pluginapi.ConfigFieldTypeString, Description: "Optional prefix added to discovered Cursor model IDs."},
				{Name: "allowed_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional allowlist of Cursor model IDs from agent models."},
				{Name: "denied_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional denylist of Cursor model IDs from agent models."},
				{Name: "excluded_model_prefixes", Type: pluginapi.ConfigFieldTypeArray, Description: "Case-insensitive model ID prefixes omitted from Cursor discovery."},
				{Name: "environment_allowlist", Type: pluginapi.ConfigFieldTypeArray, Description: "Environment variable names allowed through to the agent subprocess; CURSOR_API_KEY is always stripped."},
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum runtime for one Cursor CLI execution."},
				{Name: "max_concurrent", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum concurrent Cursor CLI subprocesses."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"openai-response", "openai-chat", "claude"},
			ExecutorOutputFormats: []string{"openai-response", "openai-chat", "claude"},
			ManagementAPI:         true,
		},
	}
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string, status int, retryable bool) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
			Retryable:  retryable,
		},
	})
	return raw
}
