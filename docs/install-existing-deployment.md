# Install Cursor provider into an existing CLIProxyAPI deployment

This plugin is `cliproxyapi-cursor` v0.2.1. It invokes only the official Cursor Agent CLI (`agent`) with direct argv. It does not proxy Cursor private endpoints and does not read or store Cursor credentials.

## Build

```sh
go test ./...
make build
```

Copy:

```text
build/plugins/linux/amd64/cliproxyapi-cursor.so
```

to the CLIProxyAPI plugin directory.

## Config block

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cliproxyapi-cursor:
      enabled: true
      priority: 100
      executable_path: "/usr/local/bin/agent"
      # Parent only; the plugin creates and cleans a fresh 0700 child per invocation.
      workspace: "/var/lib/cliproxyapi-cursor/workspaces"
      model_prefix: "cursor/"
      timeout_seconds: 120
      max_concurrent: 1
      model_cache_ttl_seconds: 600
      max_prompt_bytes: 524288
      max_request_bytes: 1048576
      max_stdout_bytes: 2097152
      max_stderr_bytes: 65536
      environment_allowlist: ["HOME", "PATH", "SHELL", "USER", "LOGNAME", "TMPDIR", "NO_COLOR", "TERM"]
```

Use a dedicated workspace parent directory. The plugin creates a fresh 0700 empty child workspace for every invocation and removes it when the official agent exits. Mount only the official Cursor CLI home/config volume needed for browser login.

## Authentication

Authenticate the official CLI, not this plugin:

```sh
NO_OPEN_BROWSER=1 agent login
agent status --format json
agent models
```

The plugin may coordinate status/login through CLIProxyAPI auth methods, but it persists only status metadata such as `authenticated: true`; it never persists tokens.

## Rollback

Remove the `cliproxyapi-cursor` config block and the plugin `.so`, restore the previous CLIProxyAPI config, then restart the deployment under your normal ops procedure.
