#!/usr/bin/env bash
set -euo pipefail

IMAGE="${CLIPROXYAPI_IMAGE:-eceasy/cli-proxy-api:v7.2.138}"
PLUGIN_SO="${PLUGIN_SO:-build/plugins/linux/amd64/cliproxyapi-cursor.so}"
MANAGEMENT_KEY="${MANAGEMENT_KEY:-test-management-key}"
API_KEY="${API_KEY:-test-api-key}"
FULL_INSTALL=0
SKIP_BUILD="${SKIP_BUILD:-0}"

usage() {
  cat <<'USAGE'
Usage: scripts/integration-cli-proxy-v72138.sh [--full-install]

Starts a disposable, resource-limited CLIProxyAPI v7.2.138 container with the
locally built cliproxyapi-cursor plugin and verifies external-host setup plus
native auth-files/model data used by the built-in quota UI. --full-install additionally performs the explicit
confirm=true official Cursor Agent package install in the disposable HOME and
runs the installed agent --version. No shared CLIProxyAPI state is touched.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --full-install) FULL_INSTALL=1 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [ "$SKIP_BUILD" != "1" ]; then
  make build
fi
if [ ! -f "$PLUGIN_SO" ]; then
  echo "plugin artifact missing: $PLUGIN_SO" >&2
  exit 1
fi

PORT=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
)
TMP=$(mktemp -d .tmp-cli-proxy-v72138-XXXXXX)
CID=""
cleanup() {
  if [ -n "$CID" ]; then
    docker logs "$CID" > "$TMP/docker.log" 2>&1 || true
    docker rm -f "$CID" >/dev/null 2>&1 || true
  fi
  echo "integration workspace: $TMP"
  echo "container log: $TMP/docker.log"
}
trap cleanup EXIT

mkdir -p "$TMP/plugins/linux/amd64" "$TMP/logs" "$TMP/auth" "$TMP/cursor-home" "$TMP/workspaces"
cp "$PLUGIN_SO" "$TMP/plugins/linux/amd64/cliproxyapi-cursor.so"
mkdir -p "$TMP/cursor-home/fake-bin"
cat >"$TMP/cursor-home/fake-bin/agent" <<'SH'
#!/bin/sh
if [ "$1" = "about" ] && [ "$2" = "--format" ] && [ "$3" = "json" ]; then
  printf '%s\n' '{"userEmail":"smoke@example.test","subscriptionTier":"Pro","cliVersion":"smoke-cli-1"}'
  exit 0
fi
if [ "$1" = "status" ]; then
  echo '{"authenticated":true}'
  exit 0
fi
if [ "$1" = "models" ]; then
  echo 'auto - Cursor Auto'
  exit 0
fi
if [ "$1" = "--version" ]; then
  echo 'Cursor Agent smoke-cli-1'
  exit 0
fi
if [ "$1" = "--print" ]; then
  if [ "$2" != "--output-format" ] || [ "$3" != "json" ] || [ "$4" != "--sandbox" ] || [ "$5" != "disabled" ]; then
    echo 'missing required print/json/sandbox flags' >&2
    exit 64
  fi
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"INTEGRATION_OK","usage":{"inputTokens":11,"outputTokens":3}}'
  exit 0
fi
exit 64
SH
chmod 755 "$TMP/cursor-home/fake-bin/agent"
cat >"$TMP/auth/cursor-cli.json" <<'JSON'
{"type":"cursor","email":"smoke@example.test","tier":"Pro","version":"smoke-cli-1","authenticated":true,"status_known":true}
JSON
python3 - "$PORT" "$TMP/config.yaml" "$API_KEY" <<'PY'
import pathlib, sys
port, out, api_key = sys.argv[1], pathlib.Path(sys.argv[2]), sys.argv[3]
text = pathlib.Path('config/config.yaml').read_text()
replacements = {
    'port: 8317': f'port: {port}',
    '__CLIPROXYAPI_API_KEY__': api_key,
    'dir: "plugins"': 'dir: "/plugins"',
    'auth-dir: "/root/.cli-proxy-api"': 'auth-dir: "/auth"',
    'executable_path: "agent"': 'executable_path: "/cursor-home/fake-bin/agent"',
    'workspace: "/var/lib/cliproxyapi-cursor/workspaces"': 'workspace: "/workspaces"',
}
for old, new in replacements.items():
    text = text.replace(old, new)
out.write_text(text)
PY

CID=$(docker run -d --rm --platform linux/amd64 \
  --name "cliproxyapi-cursor-v72138-$$" \
  -e "MANAGEMENT_PASSWORD=$MANAGEMENT_KEY" \
  -e HOME=/cursor-home \
  -p "127.0.0.1:${PORT}:${PORT}" \
  -v "$PWD/$TMP/config.yaml:/CLIProxyAPI/config.yaml:ro" \
  -v "$PWD/$TMP/plugins:/plugins:ro" \
  -v "$PWD/$TMP/logs:/CLIProxyAPI/logs" \
  -v "$PWD/$TMP/auth:/auth" \
  -v "$PWD/$TMP/cursor-home:/cursor-home" \
  -v "$PWD/$TMP/workspaces:/workspaces" \
  --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=512m \
  --tmpfs /root:rw,nosuid,nodev,size=64m \
  --memory=1536m --cpus=2 \
  "$IMAGE" ./CLIProxyAPI -config /CLIProxyAPI/config.yaml)

BASE="http://127.0.0.1:${PORT}"
for _ in $(seq 1 120); do
  if curl -fsS "$BASE/v0/resource/plugins/cliproxyapi-cursor/setup" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

RESOURCE_CODE=$(curl -sS -D "$TMP/setup.headers" -o "$TMP/setup.html" -w '%{http_code}' \
  "$BASE/v0/resource/plugins/cliproxyapi-cursor/setup")
UNAUTH_CODE=$(curl -sS -o "$TMP/status_unauth.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/setup/status")
QUOTA_RESOURCE_CODE=$(curl -sS -o "$TMP/quota.html" -w '%{http_code}' \
  "$BASE/v0/resource/plugins/cliproxyapi-cursor/quota")
AUTH_FILES_UNAUTH_CODE=$(curl -sS -o "$TMP/auth_files_unauth.json" -w '%{http_code}' \
  "$BASE/v0/management/auth-files")
AUTH_FILES_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/auth_files.json" -w '%{http_code}' \
  "$BASE/v0/management/auth-files")
AUTH_MODELS_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/auth_models.json" -w '%{http_code}' \
  "$BASE/v0/management/auth-files/models?name=cursor-cli.json")
STATUS_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/status_before.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/setup/status")
RESPONSES_CODE=$(curl -sS -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"cursor/auto","input":"Reply with INTEGRATION_OK","stream":false}' \
  -o "$TMP/responses.json" -w '%{http_code}' "$BASE/v1/responses")
STREAM_CODE=$(curl -sS -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"cursor/auto","input":"Reply with INTEGRATION_OK","stream":true}' \
  -o "$TMP/responses.stream" -w '%{http_code}' "$BASE/v1/responses")
QUOTA_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/quota.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/quota")
CONFIRM_FALSE_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"confirm":false}' -o "$TMP/install_false.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/setup/install")
CONFUSED_CODE=$(curl --path-as-is -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"confirm":false}' -o "$TMP/path_confusion.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/../cursor/setup/install")

python3 - "$RESOURCE_CODE" "$TMP/setup.headers" "$UNAUTH_CODE" "$QUOTA_RESOURCE_CODE" "$AUTH_FILES_UNAUTH_CODE" "$AUTH_FILES_CODE" "$TMP/auth_files.json" "$AUTH_MODELS_CODE" "$TMP/auth_models.json" "$STATUS_CODE" "$TMP/status_before.json" "$RESPONSES_CODE" "$TMP/responses.json" "$STREAM_CODE" "$TMP/responses.stream" "$QUOTA_CODE" "$TMP/quota.json" "$CONFIRM_FALSE_CODE" "$TMP/install_false.json" "$CONFUSED_CODE" <<'PY'
import json, pathlib, sys
resource_code, headers_path, unauth_code, quota_resource_code, auth_files_unauth_code, auth_files_code, auth_files_path, auth_models_code, auth_models_path, status_code, status_path, responses_code, responses_path, stream_code, stream_path, quota_code, quota_path, false_code, false_path, confused_code = sys.argv[1:]
headers = pathlib.Path(headers_path).read_text().lower()
auth_files = json.loads(pathlib.Path(auth_files_path).read_text())
auth_models = json.loads(pathlib.Path(auth_models_path).read_text())
status = json.loads(pathlib.Path(status_path).read_text())
responses = json.loads(pathlib.Path(responses_path).read_text())
stream = pathlib.Path(stream_path).read_text()
quota = json.loads(pathlib.Path(quota_path).read_text())
confirm_false = json.loads(pathlib.Path(false_path).read_text())
assert resource_code == '200', resource_code
assert 'content-type: text/html' in headers, headers
assert unauth_code == '401', unauth_code
assert quota_resource_code == '404', quota_resource_code
assert auth_files_unauth_code == '401', auth_files_unauth_code
assert auth_files_code == '200', (auth_files_code, auth_files)
cursor_files = [item for item in auth_files.get('files', []) if item.get('provider') == 'cursor']
assert len(cursor_files) == 1, auth_files
cursor = cursor_files[0]
assert cursor.get('name') == 'cursor-cli.json' and cursor.get('email') == 'smoke@example.test', cursor
assert cursor.get('account_type') == 'oauth' and cursor.get('account') == 'smoke@example.test', cursor
assert cursor.get('success') == 0 and cursor.get('failed') == 0 and 'recent_requests' in cursor, cursor
assert auth_models_code == '200', (auth_models_code, auth_models)
assert any(model.get('id') == 'cursor/auto' for model in auth_models.get('models', [])), auth_models
assert status_code == '200', (status_code, status)
assert status.get('installed') is False, status
assert responses_code == '200', (responses_code, responses)
assert responses.get('output', [{}])[0].get('content', [{}])[0].get('text') == 'INTEGRATION_OK', responses
assert responses.get('usage', {}).get('input_tokens') == 11, responses
assert responses.get('usage', {}).get('output_tokens') == 3, responses
assert stream_code == '200', (stream_code, stream)
assert 'response.output_text.delta' in stream and 'INTEGRATION_OK' in stream, stream
assert 'response.completed' in stream and '"input_tokens":11' in stream and '"output_tokens":3' in stream, stream
assert quota_code == '200', (quota_code, quota)
assert quota.get('provider') == 'cursor' and quota.get('tier') == 'Pro', quota
assert quota.get('remaining_quota_available') is False, quota
assert false_code == '400', (false_code, confirm_false)
assert confirm_false.get('installed') is False and 'explicit confirmation' in confirm_false.get('error', ''), confirm_false
assert confused_code == '404', confused_code
print('smoke_verified resource=200 unauth=401 quota_resource=404 auth_files=200 auth_models=200 responses=200 stream=200 quota=200 status_installed=false confirm_false=400 path_confusion=404')
PY

if [ "$FULL_INSTALL" = "1" ]; then
  INSTALL_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
    -d '{"confirm":true}' -o "$TMP/install_true.json" -w '%{http_code}' \
    "$BASE/v0/management/plugins/cursor/setup/install")
  STATUS_AFTER_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/status_after.json" -w '%{http_code}' \
    "$BASE/v0/management/plugins/cursor/setup/status")
  AGENT_VERSION=$(docker exec "$CID" /cursor-home/.local/bin/agent --version 2>&1)
  python3 - "$TMP/install_true.json" "$TMP/status_after.json" "$INSTALL_CODE" "$STATUS_AFTER_CODE" "$AGENT_VERSION" <<'PY'
import json, sys
install = json.load(open(sys.argv[1]))
status = json.load(open(sys.argv[2]))
install_code, status_code, agent_version = sys.argv[3], sys.argv[4], sys.argv[5].strip()
assert install_code == '200', (install_code, install)
assert status_code == '200', (status_code, status)
assert install.get('installed') is True, install
assert status.get('installed') is True, status
assert install.get('version') and status.get('version') == install.get('version'), (install, status)
assert install.get('package_sha256') and install.get('package_bytes', 0) > 0, install
assert agent_version, 'agent --version returned empty output'
print('full_install_verified version=%s package_sha256=%s package_bytes=%s agent_version=%s' % (
    install.get('version'), install.get('package_sha256'), install.get('package_bytes'), agent_version))
PY
fi

docker logs "$CID" > "$TMP/docker.log" 2>&1
if grep -q 'management registrar cliproxyapi-cursor failed' "$TMP/docker.log"; then
  echo 'management registrar failure found in container log' >&2
  exit 1
fi
if ! grep -q 'plugin registered plugin_id=cliproxyapi-cursor .* version=0.4.1' "$TMP/docker.log"; then
  echo 'plugin registration log for cliproxyapi-cursor v0.4.1 not found' >&2
  exit 1
fi

echo 'external_host_verified plugin_id=cliproxyapi-cursor version=0.4.1 no_registrar_failures=true native_auth_files=200 native_auth_models=200 responses=200 stream=200 quota=200'
