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

After every configuration change, and every 15 minutes, the `plugin.sync` job makes sure the configured stores are added to mihon-source (through its store API, which pins each store's signing key) and the configured extensions are installed: from the stores that offer a supported version, preferring a jar, then the newest. It only adds. Stores and extensions added in mihon-source by other means stay, and updates are installed in mihon-source. Problems show in `Health` as degraded, and in the logs, named by what failed: "store <url> couldn't be fetched: …" (mihon-source couldn't download it), "store <url> can't be used: …" (unusable content, or a key that doesn't match), "extension <pkg>: no store offers a supported version". Only when mihon-source itself can't be reached does the message say so. The same problems are the `plugin.sync` run's notes, and fail the run. With nothing to keep (no stores, extensions or health-check interval configured), the job is disabled and says so (`disabled_reason`): it neither runs nor can be started until there is.

## Jobs

With the `JOBS` capability, the job calls show the plugin's own job, `plugin.sync` (above), and then mihon-source's jobs, passed through as they are: `store-refresh` and `source-health` (params `source_ids`, `langs`, `sample`, `steps`). The plugin's own job IDs start with `plugin.` and its run IDs with `plugin-`; everything else goes to mihon-source, whose run history survives restarts (the plugin's is kept in memory). When mihon-source can't be reached, `ListJobs` lists the plugin's job alone. mihon-source records runs it starts for the plugin as started by the plugin's API client: `on_behalf_of` reaches only the plugin's own runs and the log.

## Health checks

With the `HEALTH_CHECKS` capability, `Source.health` is mihon-source's last check of the source, and `SetSourceHealthCheck` turns its scheduled checks off or on (mihon-source's `SetHealthCheck`). To check a source now, run `source-health` with `source_ids` (and `steps`): the run notes each step's result. The schedule comes from `health_checks` in the configuration: on every `plugin.sync` run, source-mihon sets mihon-source's schedule to the interval and sample asked for, with the plugin's `languages`, unless it already is; `default` hands it back to mihon-source's own settings, and an empty interval leaves it alone.

## Extensions as packages

source-mihon declares the source API's `PACKAGES` capability, so the Panelarr server shows an Extensions list for it:

- **ListPackages / GetPackage** put together, per package name, what the stores offer (each store an offer, with its version) and what mihon-source has installed (version, load error, its sources and their languages, the extension's enabled languages). The update flag is mihon-source's. Icons are served at `/v1/packages/{id}/icon` when standalone.
- **InstallPackage** installs the best supported offer (a jar first, then the newest) or the store asked for, through mihon-source's store API, which checks the signing key and checksum. The installed version again is a no-op. A cancelled install leaves the previous version or finishes; mihon-source guarantees that.
- **UpdatePackage** installs the newest supported offer when it's newer; up to date is a no-op. mihon-source refreshes store indexes on its schedule (or its store-refresh job).
- **UninstallPackage** removes the extension; one that isn't installed is a no-op.
- **SetPackageSettings** sets the extension's languages in mihon-source.
- **ListOrigins / RefreshOrigin** are mihon-source's stores: URL, last refresh and its error, extension count and pinned signing key. A refresh that fails answers `STORE_FETCH_FAILED` or `STORE_INVALID`, as installs do.
- **Package details** of an installed extension come from mihon-source: signing fingerprint, extensions-lib version (`api_version`), install time, file checksum and the store it came from. Its load error is the package's `error`.
- **ReloadPackage** reloads the extension in mihon-source from its installed file, e.g. after a failed load or to apply a setting the extension reads only when it starts.
- Store problems come back with the source API's reasons: `STORE_FETCH_FAILED` (UNAVAILABLE) and `STORE_INVALID` (FAILED_PRECONDITION).

The declarative `extensions` setting still installs missing extensions for people who configure by file.

## Languages

A source is listed when its language passes both the plugin's `languages` setting and its extension's languages (empty means all at each level). mihon-source hides sources outside an extension's languages; source-mihon applies its own setting on top. Calls to any other source answer NOT_FOUND. Each listed source names its extension as `package_id`.

## Network profiles

The host owns proxies, solvers and rate limits, and pushes a profile per source with `SetNetworkProfile`. source-mihon forwards it to mihon-source, which applies it to that source's requests (proxy, solver, rate limit, user agent, headers, timeouts) and overrides the profile assigned in mihon-source's own admin. Removing the pushed profile restores that. mihon-source keeps pushed profiles across restarts of either service.

`GetNetworkProfile` says which profile a source uses now: `PUSHED`, `PLUGIN` (mihon-source's own assignment or default) or `NONE`, without proxy passwords.

## Restarts

`Health.instance_id` combines source-mihon's own start with mihon-source's, so it changes when either restarts, and a host knows to push its configuration and profiles again.

## Source settings

Each source's settings (its Mihon preferences) pass through: `GetSettingsSchema`, `GetSettings`, `SetSettings`, for sources with the `SETTINGS` capability. The schema keeps the extension's order (`x-order`). Settings the extension shows as passwords are `writeOnly`: they read back empty, and an empty value keeps the stored one.

## Limits

`GetInfo` asks hosts to keep to 2 parallel requests and 30 requests a minute (burst 5) per source for API calls. Page images have their own budget: up to 4 parallel downloads per source and no rate limit, because mihon-source fetches images through each source's own client, which applies the extension's rate limits and a pushed profile's.

## Health and metrics

- `Health`: not serving while mihon-source can't be reached; degraded when mihon-source is (e.g. an extension failed to load) or when keeping stores and extensions failed; serving otherwise. `GET /healthz` answers while the process runs.
- `GET /metrics` (standalone, with the plugin's key): `source_mihon_calls_total{method,code,reason}`, `source_mihon_package_ops_total{op,outcome}`, `source_mihon_call_duration_seconds{method}`, `source_mihon_upstream_up`, `source_mihon_sources`, `source_mihon_store_syncs_total{outcome}`, plus Go runtime and process metrics.
- Logs are JSON on standard error. Keys and credentials are never logged.
