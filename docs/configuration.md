# Configuration

source-mihon declares its configuration as a JSON Schema in `GetInfo` (`config_schema`). The Panelarr server renders it as a form and sends the values with `SetConfig`. Standalone, the same values can come from a JSON file (`--config`) or, for the connection, from the environment.

| Key | Default | Meaning |
|---|---|---|
| `mihon_source_url` | `http://mihon-source:8902` | Where mihon-source serves its API |
| `api_key` | — | A mihon-source client key (`msk_…`), or its admin token. Needs `sources:read` and `sources:write`, plus `admin` to keep stores and extensions. A secret: never returned by `GetConfig`; sending it empty keeps the current one |
| `languages` | `[]` | Languages to offer sources in, e.g. `["en", "fr"]`. Empty offers all. `"all"` and `"multi"` are languages too: list them to keep multi-language sources. Each extension can narrow this further (its package settings). Sources outside them aren't listed and answer NOT_FOUND |
| `stores` | `[]` | Stores to add to mihon-source: `{"url": "…/repo.json", "signing_key_fingerprint": "…"}`. The fingerprint is optional and pins the store's key from the start |
| `extensions` | `[]` | Package names of extensions to install from those stores when they're missing |
| `health_checks.interval` | `""` | How often mihon-source checks its sources on its own: a number and `m`, `h` or `d` (e.g. `6h`), `off` for no scheduled checks, or `default` for mihon-source's own `MIHON_HEALTH_CHECK_*` settings. Empty leaves mihon-source as it is. The checks cover `languages` (all when empty) |
| `health_checks.sample` | `10` | Sources per round, the least recently checked first, so rounds rotate through all of them; `0` checks every source each round |

A key mihon-source rejects makes `SetConfig` fail at once. mihon-source being down doesn't: it may still be starting, and `Health` says so.

## Flags and environment

| Flag | Environment | Meaning |
|---|---|---|
| `--listen` | `PLUGIN_LISTEN` | Serve standalone on this address (default `:8904` when run by hand) |
| `--api-key` | `PLUGIN_API_KEY` | The key callers (the Panelarr server) must send (or `PLUGIN_API_KEY_FILE`). Health and `/healthz` need none |
| `--config` | `PLUGIN_CONFIG` | A JSON file with the configuration above |
| | `MIHON_SOURCE_URL` | `mihon_source_url`, without a config file |
| | `MIHON_SOURCE_API_KEY` | `api_key`, without a config file. `MIHON_SOURCE_API_KEY_FILE` reads it from a file (Docker secrets) |
| | `PLUGIN_LOG_LEVEL` | `debug`, `info` (default), `warn` or `error`. Logs are JSON on standard error |

Started by the Panelarr server (managed mode), it ignores `--listen` and `--config`: the server sends the configuration.

## Subcommands

- `source-mihon healthcheck [--url …]`: exits 0 when the local `/healthz` answers, for container health checks.
- `source-mihon version`: prints the version.

## Container

`panelarr/source-mihon` listens on 8904 as a non-root user, with a health check. [compose.example.yaml](../compose.example.yaml) runs it next to mihon-source, with the keys as Docker secrets and 8904 reachable only on the compose network. `make docker` builds the image locally.
