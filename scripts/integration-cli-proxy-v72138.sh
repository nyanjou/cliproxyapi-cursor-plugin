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
locally built cliproxyapi-cursor plugin and verifies external-host management
RPC/resource behavior. --full-install additionally performs the explicit
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
if [ "$1" = "--version" ]; then
  echo 'Cursor Agent smoke-cli-1'
  exit 0
fi
exit 64
SH
chmod 755 "$TMP/cursor-home/fake-bin/agent"
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
QUOTA_UNAUTH_CODE=$(curl -sS -o "$TMP/quota_unauth.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/quota")
QUOTA_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/quota.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/quota")
STATUS_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -o "$TMP/status_before.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/setup/status")
CONFIRM_FALSE_CODE=$(curl -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"confirm":false}' -o "$TMP/install_false.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/setup/install")
CONFUSED_CODE=$(curl --path-as-is -sS -H "Authorization: Bearer $MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"confirm":false}' -o "$TMP/path_confusion.json" -w '%{http_code}' \
  "$BASE/v0/management/plugins/cursor/../cursor/setup/install")

python3 - "$RESOURCE_CODE" "$TMP/setup.headers" "$UNAUTH_CODE" "$QUOTA_RESOURCE_CODE" "$QUOTA_UNAUTH_CODE" "$QUOTA_CODE" "$TMP/quota.json" "$TMP/quota.html" "$STATUS_CODE" "$TMP/status_before.json" "$CONFIRM_FALSE_CODE" "$TMP/install_false.json" "$CONFUSED_CODE" <<'PY'
import json, pathlib, sys
resource_code, headers_path, unauth_code, quota_resource_code, quota_unauth_code, quota_code, quota_path, quota_html_path, status_code, status_path, false_code, false_path, confused_code = sys.argv[1:]
headers = pathlib.Path(headers_path).read_text().lower()
quota = json.loads(pathlib.Path(quota_path).read_text())
quota_html = pathlib.Path(quota_html_path).read_text()
status = json.loads(pathlib.Path(status_path).read_text())
confirm_false = json.loads(pathlib.Path(false_path).read_text())
assert resource_code == '200', resource_code
assert 'content-type: text/html' in headers, headers
assert unauth_code == '401', unauth_code
assert quota_resource_code == '200', quota_resource_code
assert quota_unauth_code == '401', quota_unauth_code
assert quota_code == '200', (quota_code, quota)
assert quota.get('account') == 'smoke@example.test' and quota.get('tier') == 'Pro' and quota.get('version') == 'smoke-cli-1', quota
assert quota.get('remaining_quota', {}).get('available') is False, quota
assert 'localStorage' not in quota_html and 'sessionStorage' not in quota_html, quota_html
assert status_code == '200', (status_code, status)
assert status.get('installed') is False, status
assert false_code == '400', (false_code, confirm_false)
assert confirm_false.get('installed') is False and 'explicit confirmation' in confirm_false.get('error', ''), confirm_false
assert confused_code == '404', confused_code
print('smoke_verified resource=200 unauth=401 quota_resource=200 quota_unauth=401 quota_auth=200 status_installed=false confirm_false=400 path_confusion=404')
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
if ! grep -q 'plugin registered plugin_id=cliproxyapi-cursor .* version=0.2.1' "$TMP/docker.log"; then
  echo 'plugin registration log for cliproxyapi-cursor v0.2.1 not found' >&2
  exit 1
fi

echo 'external_host_verified plugin_id=cliproxyapi-cursor version=0.2.1 no_registrar_failures=true quota_auth=200'
