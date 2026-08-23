# CLIProxyAPI Cursor Agent CLI provider

Experimental v0.2.1 CLIProxyAPI provider for Cursor subscription models through the official `agent` CLI only.

This is not a raw Cursor inference API wrapper. It preserves Cursor's official agent harness by spawning the authenticated `agent` executable with direct argv in print/ask mode, sandbox disabled, and a fresh private empty workspace per invocation. The plugin never calls Cursor private endpoints, never reads or stores OAuth material, never creates or uses `CURSOR_API_KEY`, and never invokes a shell for model requests.

Licensed under MIT. ABI/scaffolding is derived from the MIT-licensed `arthur-sommer-etc/cliproxyapi-copilot-plugin`; Cursor-specific provider logic is separate and documented here.

## What works

- CLIProxyAPI plugin ABI v1 / registration schema 2.
- Auth provider status records for the existing browser-authenticated Cursor CLI session.
- Same-origin Cursor setup resource for missing `agent`, with explicit user confirmation before any official CLI install.
- Management-authenticated install action that fetches `https://cursor.com/install`, strictly parses the official Cursor download URL, safely extracts the tarball, verifies `cursor-agent --version`, and atomically installs under the runtime HOME/managed root.
- Login starts `NO_OPEN_BROWSER=1 agent login` directly, streams and redacts bounded output, and exposes exactly one Cursor approval URL promptly instead of waiting for process exit.
- Model discovery via `agent models`.
- Non-streaming OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages inputs converted to bounded text prompts for Cursor ask mode.
- Streaming via `agent -p --output-format stream-json --stream-partial-output`, emitted to CLIProxyAPI as bare NDJSON (`application/x-ndjson`), with duplicate partial suppression and terminal result/usage treated as canonical.
- Direct argv only, minimal environment allowlist, `CURSOR_API_KEY` stripping, process-group cancellation, stdout/stderr bounds, request/prompt bounds, runtime and concurrency limits.
- Linux amd64 store package ZIP containing exactly `cliproxyapi-cursor.so` at archive root.

## Limitations

- Cursor runs as an agent harness with fixed context/tool overhead; usage numbers are best-effort values from the Cursor CLI terminal result when present.
- Caller-supplied tools/tool schemas are rejected. The plugin does not claim Claude Code-style external tool-call compatibility.
- Image/file/audio attachments and mixed text+attachment requests are rejected before invoking Cursor; unsupported non-text content is never silently dropped.
- Raw provider HTTP proxying is forbidden; this provider does not expose Cursor endpoints.
- Authentication is the official Cursor CLI browser session. If `agent` is missing, login returns the same-origin `/v0/resource/plugins/cliproxyapi-cursor/setup` page instead of an exec error. Approval URLs may be shown; credentials, auth files, emails, and tokens must not be stored or logged by this plugin.

## Cursor quota/account visibility

The plugin exposes a native management resource named `Cursor Quota` at `/v0/resource/plugins/cliproxyapi-cursor/quota`. The static unauthenticated HTML contains no account data and keeps the CLIProxyAPI management key only in current page memory. It fetches `/v0/management/plugins/cursor/quota`, which is management-authenticated and runs only `agent about --format json` through the same bounded safe runner.

Only safe fields from official Cursor CLI output are returned (`userEmail`/account, `subscriptionTier`, and `cliVersion`/version). Numeric remaining subscription quota is explicitly reported as unavailable because the official `agent about` JSON does not expose it. The plugin does not read Cursor OAuth/token files and does not call private Cursor endpoints. Stock CPA Manager Plus has a hard-coded Quota page; CLIProxyAPI plugin ABI v7.2.138 cannot inject a section there, so this plugin resource is the supported Cursor quota/account view.

## Build and test

Requirements: Go 1.26 for tests and Docker for the production Bookworm-compatible build.

```sh
gofmt -w internal/provider/*.go cmd/cliproxyapi-cursor/*.go
go test ./...
go test -race ./...
go vet ./...
make build
scripts/integration-cli-proxy-v72138.sh
scripts/integration-cli-proxy-v72138.sh --full-install
scripts/package-release.sh 0.2.1
```

Artifacts:

```text
build/plugins/linux/amd64/cliproxyapi-cursor.so
dist/cliproxyapi-cursor_0.2.1_linux_amd64.zip
dist/checksums.txt
```

Inspect the store ZIP before publishing:

```sh
python3 - <<'PY'
import zipfile
with zipfile.ZipFile('dist/cliproxyapi-cursor_0.2.1_linux_amd64.zip') as z:
    print(z.namelist())
PY
sha256sum dist/cliproxyapi-cursor_0.2.1_linux_amd64.zip
```

## Configuration

See `config/config.yaml` for a CLIProxyAPI config fragment. Key plugin settings:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cliproxyapi-cursor:
      enabled: true
      # Prefer PATH `agent`; if missing, the plugin falls back to managed HOME/.local/bin/agent.
      executable_path: "agent"
      # Optional stricter managed install root; default is runtime HOME. Must be persistent/writable.
      managed_install_root: "/cursor-home"
      max_package_bytes: 268435456
      max_expanded_bytes: 536870912
      max_archive_entries: 10000
      # Parent directory only. The plugin creates a new 0700 empty child
      # workspace for each invocation and removes it when the agent exits.
      workspace: "/tmp/cliproxyapi-cursor-workspaces"
      model_prefix: "cursor/"
      timeout_seconds: 120
      max_concurrent: 1
      max_prompt_bytes: 524288
      max_request_bytes: 1048576
      max_stdout_bytes: 2097152
      max_stderr_bytes: 65536
      model_cache_ttl_seconds: 600
      environment_allowlist: ["HOME", "PATH", "SHELL", "USER", "LOGNAME", "TMPDIR", "NO_COLOR", "TERM"]
```

Mount a persistent runtime HOME (for `.local/share/cursor-agent`, `.local/bin`, and official Cursor auth/config) and a private workspace parent directory. The configured `workspace` is not used directly for prompts; it is a parent for per-invocation 0700 empty directories that are cleaned up after use. Keep CLIProxyAPI loopback-only unless you intentionally put it behind your own authentication boundary.

## In-plugin Cursor Agent installation

The setup page is a static unauthenticated resource only: it contains no account status, auth state, or secrets. The state-changing install endpoint is registered under CLIProxyAPI Management API and therefore requires management authentication. The page asks for the management key in page memory and posts `{ "confirm": true }` only after the user presses `Install official Cursor Agent CLI`.

Installer safety model:

- No `curl | bash`, no downloaded shell execution, and no shell for installer/package handling.
- Fetches `https://cursor.com/install` with redirect/host/body/time bounds and parses only the canonical `https://downloads.cursor.com/lab/<version>/linux/x64/agent-cli-package.tar.gz` package URL.
- Downloads package bytes with a hard size cap and SHA-256 calculation; package bytes are never logged.
- Extracts tar.gz in Go with path traversal, absolute path, hardlink, unsafe symlink, special-file, duplicate-entry, entry-count, and expanded-size checks.
- Installs atomically under `$HOME/.local/share/cursor-agent/versions/<version>` and replaces `$HOME/.local/bin/agent` / `cursor-agent` symlinks only after `--version` verifies the expected version.
- Concurrent installs are serialized and idempotent. Reinstall/update requires a fresh explicit management-authenticated confirmation.

Uninstall/rollback: remove `$HOME/.local/bin/agent`, `$HOME/.local/bin/cursor-agent`, and the desired `$HOME/.local/share/cursor-agent/versions/<version>` directory from the managed runtime HOME. Existing versions are kept until a new version verifies and activates.

Offline/manual fallback: preinstall the official Cursor Agent CLI in the runtime and set `executable_path` to that absolute path, or leave `agent` to use PATH plus managed fallback. This is separate from installing/updating the CLIProxyAPI plugin `.so` itself.

## Existing deployment / rollback

1. Build `cliproxyapi-cursor.so`.
2. Copy it to the deployment plugin directory.
3. Add/replace the `cliproxyapi-cursor` block in `plugins.configs`.
4. Restart CLIProxyAPI yourself after review. This task intentionally does not touch or restart the shared server.
5. Roll back by removing the config block and plugin file, then restoring the previous CLIProxyAPI config and restarting.

## Release workflow

Create and push a semantic tag such as `v0.2.1`, then run the **Release** workflow manually with that existing tag. The workflow validates the tag, checks out the tagged source, reruns tests, builds `cliproxyapi-cursor_<version>_linux_amd64.zip`, writes `checksums.txt`, and creates or safely replaces that tag's assets. CI is also manual or pull-request-only to avoid duplicate push/tag runs.
