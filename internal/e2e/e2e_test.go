// SPDX-License-Identifier: GPL-3.0-or-later

// Package e2e runs a real mihon-source with its lib 1.6 test extension, reading
// a made-up website served here, and checks source-mihon in front of it with
// the conformance suite. `make e2e` builds mihon-source and runs it; plain
// `go test` skips it.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/client"
	"github.com/panelarr/source-api/sdk/conformance"
	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/internal/mihon"
)

const adminToken = "e2e-admin-token"

// pngBytes is the start of a PNG, enough for content sniffing.
var pngBytes = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

var titles = map[string][]string{
	"en": {"The Lantern Keeper", "Salt and Iron", "A Quiet Orbit"},
	"fr": {"Le Gardien du Phare", "Sel et Fer", "Une Orbite Calme"},
}

type item struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Cover string `json:"cover,omitempty"`
}

func items(lang string) []item {
	var out []item
	for i, t := range titles[lang] {
		out = append(out, item{ID: fmt.Sprintf("%s-%d", lang, i+1), Title: t, Cover: fmt.Sprintf("/covers/%s-%d.png", lang, i+1)})
	}
	return out
}

func title(id string) string {
	for lang := range titles {
		for _, it := range items(lang) {
			if it.ID == id {
				return it.Title
			}
		}
	}
	return ""
}

// site is the made-up website the test extension reads: a JSON API whose
// search really searches.
func site() *httptest.Server {
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	page := func(all []item, r *http.Request) map[string]any {
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		n = max(n, 1)
		start := min((n-1)*2, len(all))
		end := min(start+2, len(all))
		return map[string]any{"items": all[start:end], "hasNext": end < len(all)}
	}
	lang := func(r *http.Request) string {
		if l := r.URL.Query().Get("lang"); l != "" {
			return l
		}
		return r.Header.Get("X-Test-Lang")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/popular", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, page(items(lang(r)), r)) })
	mux.HandleFunc("GET /api/latest", func(w http.ResponseWriter, r *http.Request) {
		all := items(lang(r))
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
		writeJSON(w, page(all, r))
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		// Like many sites, a pasted series URL finds that series. The test
		// extension's own URL resolver fails on series pages on purpose, so
		// mihon-source resolves them through search.
		_, key, isURL := strings.Cut(q, "/series/")
		isURL = isURL && strings.HasPrefix(q, "http")
		var hits []item
		for _, it := range items(lang(r)) {
			if isURL && strings.TrimSuffix(key, "/") == strings.ToLower(it.ID) || !isURL && strings.Contains(strings.ToLower(it.Title), q) {
				hits = append(hits, it)
			}
		}
		writeJSON(w, page(hits, r))
	})
	mux.HandleFunc("GET /api/series/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if title(id) == "" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"id": id, "title": title(id), "author": "A. Author", "artist": "A. Artist",
			"description": "A made-up series.", "genres": []string{"Adventure"}, "status": "ongoing", "cover": "/covers/" + id + ".png"})
	})
	mux.HandleFunc("GET /api/series/{id}/chapters", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": "c2", "name": "Chapter 2", "number": 2, "uploaded": 1700000100000},
			{"id": "c1", "name": "Chapter 1", "number": 1, "uploaded": 1700000000000},
		})
	})
	mux.HandleFunc("GET /api/chapters/{id}/pages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"index": 0, "image": "/images/0.png"}, {"index": 1, "page": "/api/page/1"}})
	})
	mux.HandleFunc("GET /api/page/{n}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"image": "/images/" + r.PathValue("n") + ".png"})
	})
	image := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append(append([]byte{}, pngBytes...), r.URL.Path...))
	}
	mux.HandleFunc("GET /images/", image)
	mux.HandleFunc("GET /covers/", image)
	return httptest.NewServer(mux)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startMihonSource runs the mihon-source distribution until the test ends.
func startMihonSource(t *testing.T, bin, siteURL string) string {
	t.Helper()
	url, _ := runMihonSource(t, bin, siteURL, filepath.Join(t.TempDir(), "data"), freePort(t))
	return url
}

// runMihonSource runs mihon-source on dataDir and port until stop is called
// or the test ends, so a test can restart it on the same data.
func runMihonSource(t *testing.T, bin, siteURL, dataDir string, port int) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"MIHON_DATA_DIR="+dataDir,
		"MIHON_HOST=127.0.0.1",
		"MIHON_PORT="+strconv.Itoa(port),
		"MIHON_ADMIN_TOKEN="+adminToken,
		"MIHON_LOG_FORMAT=text",
		"MIHON_STORE_REFRESH_INTERVAL=off",
		"JAVA_OPTS=-Dpanelarr.testext.baseUrl="+siteURL,
	)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Logf("mihon-source output:\n%s", logs.String())
		}
	})
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(90 * time.Second)
	for {
		res, err := http.Get(url + "/readyz") //nolint:noctx // test
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return url, stop
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mihon-source didn't become ready:\n%s", logs.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// install uploads the extension through mihon-source's admin API.
func install(t *testing.T, mihonURL, jar string) {
	t.Helper()
	data, err := os.ReadFile(jar) //nolint:gosec // test input
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filepath.Base(jar))
	_, _ = fw.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, mihonURL+"/v1/admin/extensions/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+adminToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("install: HTTP %d: %s", res.StatusCode, out)
	}
}

func TestEndToEnd(t *testing.T) {
	bin, jar := os.Getenv("SOURCE_MIHON_E2E_MIHON_SOURCE"), os.Getenv("SOURCE_MIHON_E2E_EXTENSION")
	if bin == "" || jar == "" {
		t.Skip("run with make e2e (needs a mihon-source build and its test extension)")
	}
	web := site()
	defer web.Close()
	mihonURL := startMihonSource(t, bin, web.URL)
	install(t, mihonURL, jar)

	// source-mihon, standalone, in front of it.
	p, err := mihon.New(context.Background(), mihon.Options{Initial: map[string]any{"mihon_source_url": mihonURL, "api_key": adminToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	srv := httptest.NewUnstartedServer(plugin.NewHandler(p, "plugin-key"))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	c := client.Remote(srv.URL, client.RemoteOptions{APIKey: "plugin-key"})

	rep := conformance.Run(t, c, conformance.Options{Timeout: 3 * time.Minute})
	if len(rep.Sources) != 2 {
		t.Fatalf("expected the test extension's 2 sources, got %d", len(rep.Sources))
	}
	for _, s := range rep.Sources {
		t.Logf("source %s %s (%s):", s.ID, s.Name, s.Lang)
		for _, st := range s.Steps {
			t.Logf("  %-14s ok=%-5v skipped=%-5v %s", st.Name, st.OK, st.Skipped, st.Detail)
		}
		if !s.OK() {
			t.Errorf("source %s failed a step", s.ID)
		}
	}
	for _, w := range rep.Warnings {
		t.Errorf("warning: %s", w)
	}

	// Search by title finds the series in every language: what matching needs.
	ctx := context.Background()
	for _, s := range rep.Sources {
		want := titles[s.Lang][1]
		res, err := c.Search(ctx, &sourcev1.SearchRequest{SourceId: s.ID, Query: strings.ToLower(want), Page: 1})
		if err != nil || len(res.GetSeries()) != 1 || res.GetSeries()[0].GetTitle() != want {
			t.Errorf("search %q on %s: %v %v", want, s.ID, res.GetSeries(), err)
		}
	}

	// Health comes through, and a pushed profile reaches mihon-source.
	h, err := c.Health(ctx)
	if err != nil || h.GetStatus() != sourcev1.HealthStatus_HEALTH_STATUS_SERVING {
		t.Errorf("health: %v %v", h, err)
	}
	if err := c.SetNetworkProfile(ctx, rep.Sources[0].ID, &sourcev1.NetworkProfile{Name: "e2e", Concurrency: 1}); err != nil {
		t.Errorf("profile: %v", err)
	}
	if err := c.SetNetworkProfile(ctx, rep.Sources[0].ID, nil); err != nil {
		t.Errorf("profile removal: %v", err)
	}

	// A store mihon-source can't fetch is the store's problem, not mihon-source's.
	missing := web.URL + "/no-store/repo.json"
	before := time.Now()
	if err := p.Set(ctx, map[string]any{"mihon_source_url": mihonURL, "api_key": adminToken, "stores": []any{map[string]any{"url": missing}}}); err != nil {
		t.Fatal(err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := p.Synced(syncCtx, before); err != nil {
		t.Fatal(err)
	}
	if got := p.StoreProblem(); !strings.HasPrefix(got, "store "+missing+" couldn't be fetched: ") || strings.Contains(got, "can't be reached") {
		t.Errorf("store problem: %q", got)
	}
	t.Logf("store problem: %s", p.StoreProblem())
}
