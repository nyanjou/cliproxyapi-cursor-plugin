# CLIProxyAPI Cursor Agent CLI provider

Experimental v0.1.0 CLIProxyAPI provider for Cursor subscription models through the official `agent` CLI only.

This is not a raw Cursor inference API wrapper. It preserves Cursor's official agent harness by spawning the authenticated `agent` executable with direct argv in print/ask mode, sandbox enabled, and a fresh private empty workspace per invocation. The plugin never calls Cursor private endpoints, never reads or stores OAuth material, never creates or uses `CURSOR_API_KEY`, and never invokes a shell for model requests.

Licensed under MIT. ABI/scaffolding is derived from the MIT-licensed `arthur-sommer-etc/cliproxyapi-copilot-plugin`; Cursor-specific provider logic is separate and documented here.

## What works

- CLIProxyAPI plugin ABI v1 / registration schema 2.
- Auth provider status records for the existing browser-authenticated Cursor CLI session.
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
- Authentication is the official Cursor CLI browser session. Headless login should be done with `NO_OPEN_BROWSER=1 agent login`; approval URLs/states may be shown, credentials must not be stored by this plugin.

## Build and test

Requirements: Go 1.26 for tests and Docker for the production Bookworm-compatible build.

```sh
gofmt -w internal/provider/*.go cmd/cliproxyapi-cursor/*.go
go test ./...
go test -race ./...
go vet ./...
make build
scripts/package-release.sh 0.1.0
```

Artifacts:

```text
build/plugins/linux/amd64/cliproxyapi-cursor.so
dist/cliproxyapi-cursor_0.1.0_linux_amd64.zip
dist/checksums.txt
```

Inspect the store ZIP before publishing:

```sh
python3 - <<'PY'
import zipfile
with zipfile.ZipFile('dist/cliproxyapi-cursor_0.1.0_linux_amd64.zip') as z:
    print(z.namelist())
PY
sha256sum dist/cliproxyapi-cursor_0.1.0_linux_amd64.zip
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
      executable_path: "/Users/snupai/.local/bin/agent"
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

Mount only the Cursor auth/config volume needed by the official CLI and a private workspace parent directory. The configured `workspace` is not used directly for prompts; it is a parent for per-invocation 0700 empty directories that are cleaned up after use. Keep CLIProxyAPI loopback-only unless you intentionally put it behind your own authentication boundary.

## Existing deployment / rollback

1. Build `cliproxyapi-cursor.so`.
2. Copy it to the deployment plugin directory.
3. Add/replace the `cliproxyapi-cursor` block in `plugins.configs`.
4. Restart CLIProxyAPI yourself after review. This task intentionally does not touch or restart the shared server.
5. Roll back by removing the config block and plugin file, then restoring the previous CLIProxyAPI config and restarting.

## Release workflow

Tag format is semantic dotted numeric with `v` prefix, e.g. `v0.1.0`. The release workflow builds and publishes `cliproxyapi-cursor_<version>_linux_amd64.zip` and `checksums.txt`.
