# Configuration

source-mihon declares its configuration as a JSON Schema in `GetInfo` (`config_schema`). The Panelarr server renders it as a form and sends the values with `SetConfig`. Standalone, the same values can come from a JSON file (`--config`) or, for the connection, from the environment.

| Key | Default | Meaning |
|---|---|---|
| `mihon_source_url` | `http://mihon-source:8902` | Where mihon-source serves its API |
| `api_key` | — | A mihon-source client key (`msk_…`), or its admin token. Needs `sources:read` and `sources:write`, plus `admin` to keep stores and extensions. A secret: never returned by `GetConfig`; sending it empty keeps the current one |
| `stores` | `[]` | Stores to add to mihon-source: `{"url": "…/repo.json", "signing_key_fingerprint": "…"}`. The fingerprint is optional and pins the store's key from the start |
| `extensions` | `[]` | Package names of extensions to install from those stores when they're missing |

A key mihon-source rejects makes `SetConfig` fail at once. mihon-source being down doesn't: it may still be starting, and `Health` says so.

## Flags and environment

| Flag | Environment | Meaning |
|---|---|---|
| `--listen` | `PLUGIN_LISTEN` | Serve standalone on this address (default `:8904` when run by hand) |
| `--api-key` | `PLUGIN_API_KEY` | The key callers (the Panelarr server) must send. Health and `/healthz` need none |
| `--config` | `PLUGIN_CONFIG` | A JSON file with the configuration above |
| | `MIHON_SOURCE_URL` | `mihon_source_url`, without a config file |
| | `MIHON_SOURCE_API_KEY` | `api_key`, without a config file. `MIHON_SOURCE_API_KEY_FILE` reads it from a file (Docker secrets) |
| | `PLUGIN_LOG_LEVEL` | `debug`, `info` (default), `warn` or `error`. Logs are JSON on standard error |

Started by the Panelarr server (managed mode), it ignores `--listen` and `--config`: the server sends the configuration.

## Subcommands

- `source-mihon healthcheck [--url …]`: exits 0 when the local `/healthz` answers, for container health checks.
- `source-mihon version`: prints the version.
