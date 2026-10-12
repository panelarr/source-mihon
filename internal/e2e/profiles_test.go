// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/client"
	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/internal/mihon"
)

// recordingProxy is a plain HTTP forward proxy that counts what goes through it.
type recordingProxy struct {
	srv  *httptest.Server
	seen atomic.Int32
}

func newProxy(t *testing.T) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		p.seen.Add(1)
		out := r.Clone(r.Context())
		out.RequestURI = ""
		res, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// fakeSolver answers FlareSolverr's API with a clearance cookie.
func fakeSolver(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cmd string `json:"cmd"`
			URL string `json:"url"`
		}
		if r.URL.Path != "/v1" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Cmd != "request.get" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{
			"url": req.URL, "status": 200, "userAgent": "FakeSolver/1", "response": "<html></html>",
			"cookies": []map[string]any{{"name": "cf_clearance", "value": "ok", "domain": "127.0.0.1", "path": "/"}},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// challenging puts an anti-bot challenge in front of the latest list until
// the request carries the solver's clearance cookie.
func challenging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			if c, err := r.Cookie("cf_clearance"); err != nil || c.Value != "ok" {
				w.Header().Set("cf-mitigated", "challenge")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, "<html><title>Just a moment...</title></html>")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func TestEndToEndProfilesAndSettings(t *testing.T) {
	bin, jar := os.Getenv("SOURCE_MIHON_E2E_MIHON_SOURCE"), os.Getenv("SOURCE_MIHON_E2E_EXTENSION")
	if bin == "" || jar == "" {
		t.Skip("run with make e2e (needs a mihon-source build and its test extension)")
	}
	base := site()
	defer base.Close()
	web := httptest.NewServer(challenging(base.Config.Handler))
	defer web.Close()
	dataDir, port := filepath.Join(t.TempDir(), "data"), freePort(t)
	mihonURL, stopMihon := runMihonSource(t, bin, web.URL, dataDir, port)
	install(t, mihonURL, jar)

	ctx := context.Background()
	config := map[string]any{"mihon_source_url": mihonURL, "api_key": adminToken}
	serve := func() (*mihon.Plugin, client.Client) {
		p, err := mihon.New(ctx, mihon.Options{Initial: config})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(plugin.NewHandler(p, "plugin-key"))
		srv.Config.Protocols = new(http.Protocols)
		srv.Config.Protocols.SetHTTP1(true)
		srv.Config.Protocols.SetUnencryptedHTTP2(true)
		srv.Start()
		t.Cleanup(srv.Close)
		t.Cleanup(p.Close)
		return p, client.Remote(srv.URL, client.RemoteOptions{APIKey: "plugin-key"})
	}
	p1, c := serve()
	list, err := c.ListSources(ctx, &sourcev1.ListSourcesRequest{Lang: "en"})
	if err != nil || len(list.GetSources()) != 1 {
		t.Fatalf("sources: %v %v", list, err)
	}
	en := list.GetSources()[0].GetId()
	popular := func(page uint32) error {
		_, err := c.Popular(ctx, &sourcev1.PopularRequest{SourceId: en, Page: page})
		return err
	}
	origin := func(c client.Client) sourcev1.NetworkProfileOrigin {
		res, err := c.GetNetworkProfile(ctx, en)
		if err != nil {
			t.Fatal(err)
		}
		return res.GetOrigin()
	}

	// A pushed proxy carries the source's requests.
	proxy := newProxy(t)
	if err := c.SetNetworkProfile(ctx, en, &sourcev1.NetworkProfile{Name: "via-proxy", Proxy: &sourcev1.Proxy{Url: proxy.srv.URL}}); err != nil {
		t.Fatal(err)
	}
	if err := popular(1); err != nil {
		t.Fatal(err)
	}
	if proxy.seen.Load() == 0 {
		t.Fatal("the pushed proxy wasn't used")
	}
	if got, _ := c.GetNetworkProfile(ctx, en); got.GetOrigin() != sourcev1.NetworkProfileOrigin_NETWORK_PROFILE_ORIGIN_PUSHED || got.GetProfile().GetProxy().GetUrl() != proxy.srv.URL {
		t.Fatalf("profile in use: %v", got)
	}

	// Removing it falls back to mihon-source's own setting: direct here.
	if err := c.SetNetworkProfile(ctx, en, nil); err != nil {
		t.Fatal(err)
	}
	through := proxy.seen.Load()
	if err := popular(2); err != nil {
		t.Fatal(err)
	}
	if proxy.seen.Load() != through || origin(c) != sourcev1.NetworkProfileOrigin_NETWORK_PROFILE_ORIGIN_NONE {
		t.Fatalf("still proxied after removal (%d → %d), origin %v", through, proxy.seen.Load(), origin(c))
	}

	// A challenge fails without a solver, and passes with a pushed one.
	_, err = c.Latest(ctx, &sourcev1.LatestRequest{SourceId: en, Page: 1})
	if plugin.ErrorReason(err) != plugin.ReasonChallenge {
		t.Fatalf("challenge without a solver: %v", err)
	}
	var solves atomic.Int32
	solver := fakeSolver(t, &solves)
	if err := c.SetNetworkProfile(ctx, en, &sourcev1.NetworkProfile{Name: "solved",
		Solver: &sourcev1.Solver{Type: sourcev1.SolverType_SOLVER_TYPE_FLARESOLVERR, Url: solver.URL}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Latest(ctx, &sourcev1.LatestRequest{SourceId: en, Page: 1}); err != nil || solves.Load() == 0 {
		t.Fatalf("with a solver: %v (solves %d)", err, solves.Load())
	}

	// The pushed profile lives in mihon-source: a new source-mihon sees it.
	p1.Close()
	p2, c2 := serve()
	if origin(c2) != sourcev1.NetworkProfileOrigin_NETWORK_PROFILE_ORIGIN_PUSHED {
		t.Fatal("the pushed profile didn't survive a source-mihon restart")
	}

	// instance_id: a new value when mihon-source restarts; the profile stays.
	before, err := p2.Health(ctx, &sourcev1.HealthRequest{})
	if err != nil || before.GetInstanceId() == "" {
		t.Fatalf("health: %v %v", before, err)
	}
	stopMihon()
	_, _ = runMihonSource(t, bin, web.URL, dataDir, port)
	after, _ := p2.Health(ctx, &sourcev1.HealthRequest{})
	if after.GetInstanceId() == "" || after.GetInstanceId() == before.GetInstanceId() {
		t.Fatalf("instance_id didn't change with mihon-source's restart: %q → %q", before.GetInstanceId(), after.GetInstanceId())
	}
	if origin(c2) != sourcev1.NetworkProfileOrigin_NETWORK_PROFILE_ORIGIN_PUSHED {
		t.Fatal("the pushed profile didn't survive a mihon-source restart")
	}

	// Settings through the source API, with the secret written but not read back.
	schema, err := c2.GetSettingsSchema(ctx, &sourcev1.GetSettingsSchemaRequest{SourceId: en})
	if err != nil {
		t.Fatal(err)
	}
	token := schema.GetSchema().GetFields()["properties"].GetStructValue().GetFields()["api_token"].GetStructValue().GetFields()
	if !token["writeOnly"].GetBoolValue() {
		t.Fatalf("api_token isn't writeOnly: %v", token)
	}
	vals, _ := structpb.NewStruct(map[string]any{"api_token": "s3cret", "show_adult": true})
	set, err := c2.SetSettings(ctx, &sourcev1.SetSettingsRequest{SourceId: en, Values: vals})
	if err != nil || set.GetValues().GetFields()["api_token"].GetStringValue() != "" || !set.GetValues().GetFields()["show_adult"].GetBoolValue() {
		t.Fatalf("set settings: %v %v", set, err)
	}
	got, err := c2.GetSettings(ctx, &sourcev1.GetSettingsRequest{SourceId: en})
	if err != nil || got.GetValues().GetFields()["api_token"].GetStringValue() != "" || !got.GetValues().GetFields()["show_adult"].GetBoolValue() {
		t.Fatalf("get settings: %v %v", got, err)
	}
	t.Logf("proxied requests %d, solves %d, instance %s → %s", proxy.seen.Load(), solves.Load(), before.GetInstanceId(), after.GetInstanceId())
}
