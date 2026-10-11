// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"io"
	"net/http"
	"strconv"
)

// maxIconBytes bounds an icon passed through.
const maxIconBytes = 1 << 20

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
		// Only the configured mihon-source is called, and the source ID is a number.
		target := u.url + "/v1/sources/" + strconv.FormatInt(id, 10) + "/icon"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil) //nolint:gosec // see above
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
	})
}
