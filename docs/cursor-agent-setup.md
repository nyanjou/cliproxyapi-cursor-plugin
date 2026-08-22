# Cursor Agent CLI deployment notes

This is a safe example for using `cliproxyapi-cursor` with CLIProxyAPI. It intentionally does not deploy or log in during the build.

## Boundary

- Use only the official Cursor Agent CLI executable.
- Do not set `CURSOR_API_KEY`; the plugin strips it from subprocess env.
- Do not expose CLIProxyAPI publicly from this compose example; port binding is loopback only.
- Keep Cursor CLI auth/config in its own mounted volume and requests in a dedicated workspace parent; the plugin creates a fresh private empty child workspace for each invocation.

## Prepare secrets/config

```sh
mkdir -p .runtime
umask 077
{
  printf 'MANAGEMENT_PASSWORD=%s\n' "$(openssl rand -hex 32)"
  printf 'CLIPROXYAPI_API_KEY=%s\n' "$(openssl rand -hex 32)"
} > .runtime/secrets.env
python3 - <<'PY'
from pathlib import Path
secret = dict(line.strip().split('=', 1) for line in Path('.runtime/secrets.env').read_text().splitlines())
text = Path('config/config.yaml').read_text().replace('__CLIPROXYAPI_API_KEY__', secret['CLIPROXYAPI_API_KEY'])
Path('.runtime/config.yaml').write_text(text)
PY
chmod 600 .runtime/secrets.env .runtime/config.yaml
```

## Build plugin

```sh
go test ./...
make build
```

## Cursor CLI setup inside CLIProxyAPI

The compose file uses `eceasy/cli-proxy-api:7.2.138` as the base and mounts `/cursor-home` persistently. Do not bake a curl-piped installer into the image. Start CLIProxyAPI, open the Cursor provider login flow, and if `agent` is absent the plugin returns the same-origin setup page. Press `Install official Cursor Agent CLI` only after reading the warning and entering the management key. The plugin then fetches and verifies the official package without executing shell installer code.

## Login

After setup succeeds, press Continue login or start the provider login again. The plugin runs `NO_OPEN_BROWSER=1 agent login`, exposes the Cursor approval URL promptly, and polling completes only after `agent status` confirms authentication. For manual fallback, authenticate in the mounted Cursor home volume with:

```sh
docker compose --env-file .runtime/secrets.env run --rm cliproxyapi sh -lc 'NO_OPEN_BROWSER=1 agent login'
docker compose --env-file .runtime/secrets.env run --rm cliproxyapi sh -lc 'agent status --format json && agent models'
```

Then start or continue CLIProxyAPI:

```sh
docker compose --env-file .runtime/secrets.env up -d
```

## Verification

Use CLIProxyAPI locally with a model discovered under the configured `model_prefix` such as `cursor/auto`. Verify non-streaming and streaming text only. Tool schemas should fail with an explicit unsupported error.
