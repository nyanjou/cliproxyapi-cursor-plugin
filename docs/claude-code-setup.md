# Cursor Agent CLI deployment notes

This is a safe example for using `cliproxyapi-cursor` with CLIProxyAPI. It intentionally does not deploy or log in during the build.

## Boundary

- Use only the official Cursor Agent CLI executable.
- Do not set `CURSOR_API_KEY`; the plugin strips it from subprocess env.
- Do not expose CLIProxyAPI publicly from this compose example; port binding is loopback only.
- Keep Cursor CLI auth/config in its own mounted volume and requests in a dedicated empty workspace.

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

## Cursor CLI install inside a custom image

The compose file uses `eceasy/cli-proxy-api:7.2.118` as the base. For production, build a derived image that installs the official Cursor CLI with an inspected, pinned installer and checksum. Do not curl-pipe uninspected scripts. Confirm inside the image:

```sh
agent --version
agent --help
```

## Login

After the image includes `agent`, authenticate the CLI in the mounted Cursor home volume:

```sh
docker compose --env-file .runtime/secrets.env run --rm cliproxyapi sh -lc 'NO_OPEN_BROWSER=1 agent login'
docker compose --env-file .runtime/secrets.env run --rm cliproxyapi sh -lc 'agent status --format json && agent models'
```

Only then start CLIProxyAPI:

```sh
docker compose --env-file .runtime/secrets.env up -d
```

## Verification

Use CLIProxyAPI locally with a model discovered under the configured `model_prefix` such as `cursor/auto`. Verify non-streaming and streaming text only. Tool schemas should fail with an explicit unsupported error.
