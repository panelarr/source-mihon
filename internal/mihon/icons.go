// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// maxIconBytes bounds an icon passed through.
const maxIconBytes = 1 << 20

// PackageIconHandler serves GET /v1/packages/{packageId}/icon: the installed
// extension's icon, or the icon its store lists.
func (p *Plugin) PackageIconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, v, err := p.lookup(r.Context(), r.PathValue("packageId"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Package IDs match plugin.PackageIDPattern, so they are safe in a path.
		path := "/v1/admin/extensions/" + v.id + "/icon"
		if v.installed == nil {
			o := v.newest()
			if o == nil {
				http.NotFound(w, r)
				return
			}
			path = "/v1/admin/stores/" + url.PathEscape(o.GetStoreId()) + "/extensions/" + v.id + "/icon"
		}
		p.proxyIcon(w, r, u, path)
	})
}

// IconHandler serves GET /v1/sources/{sourceId}/icon, the path mihon-source
// gives as a source's icon URL, by fetching it from mihon-source. Hosts
// fetch icon paths relative to the plugin with the plugin's own key.
func (p *Plugin) IconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := p.upstream()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("sourceId"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p.proxyIcon(w, r, u, "/v1/sources/"+strconv.FormatInt(id, 10)+"/icon")
	})
}

// proxyIcon fetches an icon path from mihon-source with the plugin's key.
func (p *Plugin) proxyIcon(w http.ResponseWriter, r *http.Request, u *upstream, path string) {
	// Only the configured mihon-source is called, at a path built from checked parts.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.url+path, nil) //nolint:gosec // see above
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if u.key != "" {
		req.Header.Set("Authorization", "Bearer "+u.key)
	}
	res, err := u.http.Do(req) //nolint:gosec // see above
	if err != nil {
		http.Error(w, "mihon-source can't be reached", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, http.StatusText(res.StatusCode), res.StatusCode)
		return
	}
	for _, h := range []string{"Content-Type", "Cache-Control", "ETag"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	_, _ = io.Copy(w, io.LimitReader(res.Body, maxIconBytes))
}
