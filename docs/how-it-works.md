# How it works

## Pass-through

Every `panelarr.source.v1` call goes to mihon-source over gRPC with the API key: sources, browsing, search, URL resolving, series, chapters, pages, images (streamed), settings and network profiles. Source IDs, keys and results are mihon-source's, unchanged, including each source's health from mihon-source's own checks.

- **Retries:** while mihon-source can't be reached, a call is tried three times (after 200 ms and 800 ms). Failures of a source behind mihon-source (a site down, a challenge) are not retried here: mihon-source already deals with them, and the host decides what to retry.
- **Caching:** mihon-source caches results (lists, details, pages, filters) and accepts `refresh` on series and chapters, so source-mihon adds no cache of its own.
- **Icons:** mihon-source gives icon paths relative to itself. Standalone, source-mihon serves the same paths (`GET /v1/sources/{id}/icon`, with the plugin's key) by fetching them from mihon-source. Managed plugins have no address, so those icons are left out.

## Errors

mihon-source's errors pass through with their code, reason (`SOURCE_DOWN`, `RATE_LIMITED` with its retry hint, `CLOUDFLARE_CHALLENGE`, `NOT_FOUND`, `LOGIN_REQUIRED`, `UNSUPPORTED`) and message. Problems with mihon-source itself are explained:

| Situation | Answer |
|---|---|
| mihon-source can't be reached | `UNAVAILABLE`, reason `SOURCE_DOWN`, "mihon-source at … can't be reached" |
| the API key is rejected | `FAILED_PRECONDITION`, "check the plugin's api_key" |
| the key lacks a scope | `FAILED_PRECONDITION`, naming the call |

## Stores and extensions

After every configuration change, and every 15 minutes, source-mihon makes sure the configured stores are added to mihon-source (through its store API, which pins each store's signing key) and the configured extensions are installed: from the stores that offer a supported version, preferring a jar, then the newest. It only adds. Stores and extensions added in mihon-source by other means stay, and updates are installed in mihon-source. Problems show in `Health` as degraded, and in the logs, named by what failed: "store <url> couldn't be fetched: …" (mihon-source couldn't download it), "store <url> can't be used: …" (unusable content, or a key that doesn't match), "extension <pkg>: no store offers a supported version". Only when mihon-source itself can't be reached does the message say so.

## Network profiles

The host owns proxies, solvers and rate limits, and pushes a profile per source with `SetNetworkProfile`. source-mihon forwards it to mihon-source, which applies it to that source's requests and overrides the profile assigned in mihon-source's own admin. Removing the pushed profile restores that.

## Limits

`GetInfo` asks hosts to keep to 2 parallel requests and 30 requests a minute (burst 5) per source. mihon-source applies each extension's own rate limits and the profile's on top.

## Health and metrics

- `Health`: not serving while mihon-source can't be reached; degraded when mihon-source is (e.g. an extension failed to load) or when keeping stores and extensions failed; serving otherwise. `GET /healthz` answers while the process runs.
- `GET /metrics` (standalone, with the plugin's key): `source_mihon_calls_total{method,code,reason}`, `source_mihon_call_duration_seconds{method}`, `source_mihon_upstream_up`, `source_mihon_sources`, `source_mihon_store_syncs_total{outcome}`, plus Go runtime and process metrics.
- Logs are JSON on standard error. Keys and credentials are never logged.
