// SPDX-License-Identifier: GPL-3.0-or-later

package mihon_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/client"
	"github.com/panelarr/source-api/sdk/conformance"
	"github.com/panelarr/source-api/sdk/plugin"
	"github.com/panelarr/source-api/sdk/plugintest"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
	"github.com/panelarr/source-mihon/gen/panelarr/mihon/v1/mihonv1connect"
	"github.com/panelarr/source-mihon/internal/mihon"
)

const key = "msk_test-key"

// fakeStores is mihon-source's store API: one store already added, and
// what it offers.
type fakeStores struct {
	mihonv1connect.UnimplementedMihonStoresServiceHandler
	mu         sync.Mutex
	stores     []*mihonv1.Store
	available  []*mihonv1.AvailableExtension
	added      []string
	installs   []string
	addErr     error
	installErr error
	refreshErr error
	admin      *fakeAdmin
}

func (f *fakeStores) ListStores(context.Context, *connect.Request[mihonv1.ListStoresRequest]) (*connect.Response[mihonv1.ListStoresResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&mihonv1.ListStoresResponse{Stores: f.stores}), nil
}

func (f *fakeStores) AddStore(_ context.Context, r *connect.Request[mihonv1.AddStoreRequest]) (*connect.Response[mihonv1.AddStoreResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	st := &mihonv1.Store{Id: "s" + string(rune('0'+len(f.stores))), Url: r.Msg.GetUrl(), Name: "Added"}
	f.stores = append(f.stores, st)
	f.added = append(f.added, r.Msg.GetUrl()+"|"+r.Msg.GetExpectedSigningKeyFingerprint())
	return connect.NewResponse(&mihonv1.AddStoreResponse{Store: st}), nil
}

func (f *fakeStores) RefreshStore(_ context.Context, r *connect.Request[mihonv1.RefreshStoreRequest]) (*connect.Response[mihonv1.RefreshStoreResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, st := range f.stores {
		if st.GetId() == r.Msg.GetStoreId() {
			if f.refreshErr != nil {
				st.LastError = f.refreshErr.Error()
				return nil, f.refreshErr
			}
			st.LastError, st.RefreshedAt = "", timestamppb.Now()
			return connect.NewResponse(&mihonv1.RefreshStoreResponse{Store: st}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("store not found"))
}

func (f *fakeStores) ListAvailable(context.Context, *connect.Request[mihonv1.ListAvailableRequest]) (*connect.Response[mihonv1.ListAvailableResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&mihonv1.ListAvailableResponse{Extensions: f.available}), nil
}

func (f *fakeStores) InstallFromStore(_ context.Context, r *connect.Request[mihonv1.InstallFromStoreRequest]) (*connect.Response[mihonv1.InstallFromStoreResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.installErr != nil {
		return nil, f.installErr
	}
	f.installs = append(f.installs, r.Msg.GetStoreId()+"/"+r.Msg.GetPkgName())
	var offer *mihonv1.AvailableExtension
	for _, e := range f.available {
		if e.GetPkgName() == r.Msg.GetPkgName() {
			e.Installed = true
			if e.GetStoreId() == r.Msg.GetStoreId() {
				offer = e
			}
		}
	}
	ext := &mihonv1.Extension{PkgName: r.Msg.GetPkgName(), VersionName: "1.0"}
	if offer != nil {
		ext = &mihonv1.Extension{PkgName: offer.GetPkgName(), Name: offer.GetName(), VersionName: offer.GetVersionName(), VersionCode: offer.GetVersionCode()}
		for _, s := range offer.GetSources() {
			ext.Sources = append(ext.Sources, &mihonv1.ExtensionSource{Id: s.GetId(), Name: s.GetName(), Lang: s.GetLang(), Available: true})
		}
	}
	if f.admin != nil {
		f.admin.put(ext)
	}
	return connect.NewResponse(&mihonv1.InstallFromStoreResponse{Extension: ext}), nil
}

// fakeAdmin is mihon-source's extension API: what's installed.
type fakeAdmin struct {
	mihonv1connect.UnimplementedMihonAdminServiceHandler
	mu         sync.Mutex
	installed  []*mihonv1.Extension
	uninstalls []string
	reloads    []string
	langs      map[string][]string
}

func (f *fakeAdmin) put(ext *mihonv1.Extension) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = slices.DeleteFunc(f.installed, func(e *mihonv1.Extension) bool { return e.GetPkgName() == ext.GetPkgName() })
	f.installed = append(f.installed, ext)
}

func (f *fakeAdmin) ListExtensions(context.Context, *connect.Request[mihonv1.ListExtensionsRequest]) (*connect.Response[mihonv1.ListExtensionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&mihonv1.ListExtensionsResponse{Extensions: slices.Clone(f.installed)}), nil
}

func (f *fakeAdmin) UninstallExtension(_ context.Context, r *connect.Request[mihonv1.UninstallExtensionRequest]) (*connect.Response[mihonv1.UninstallExtensionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uninstalls = append(f.uninstalls, r.Msg.GetPkgName())
	f.installed = slices.DeleteFunc(f.installed, func(e *mihonv1.Extension) bool { return e.GetPkgName() == r.Msg.GetPkgName() })
	return connect.NewResponse(&mihonv1.UninstallExtensionResponse{}), nil
}

func (f *fakeAdmin) ReloadExtension(_ context.Context, r *connect.Request[mihonv1.ReloadExtensionRequest]) (*connect.Response[mihonv1.ReloadExtensionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads = append(f.reloads, r.Msg.GetPkgName())
	for _, e := range f.installed {
		if e.GetPkgName() == r.Msg.GetPkgName() {
			return connect.NewResponse(&mihonv1.ReloadExtensionResponse{Extension: e}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("extension not found"))
}

func (f *fakeAdmin) SetExtensionLanguages(_ context.Context, r *connect.Request[mihonv1.SetExtensionLanguagesRequest]) (*connect.Response[mihonv1.SetExtensionLanguagesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.langs == nil {
		f.langs = map[string][]string{}
	}
	f.langs[r.Msg.GetPkgName()] = r.Msg.GetEnabledLangs()
	for _, e := range f.installed {
		if e.GetPkgName() == r.Msg.GetPkgName() {
			e.EnabledLangs = r.Msg.GetEnabledLangs()
		}
	}
	return connect.NewResponse(&mihonv1.SetExtensionLanguagesResponse{}), nil
}

// upstream is a fake mihon-source: the source API (the SDK's fake plugin),
// the store API and the icon route, all behind the key.
type upstream struct {
	url    string
	fake   *plugintest.Fake
	stores *fakeStores
	admin  *fakeAdmin
	srv    *httptest.Server
}

func newUpstream(t *testing.T, p plugin.Plugin) *upstream {
	t.Helper()
	f, _ := p.(*plugintest.Fake)
	if f == nil {
		f = plugintest.New()
	}
	// The fake's two sources come from two installed extensions.
	admin := &fakeAdmin{installed: []*mihonv1.Extension{
		{PkgName: "example.comics", Name: "Example Comics", VersionName: "1.0", VersionCode: 10, LibVersion: "1.5",
			SigningFingerprint: "ab12", Origin: &mihonv1.ExtensionOrigin{Kind: &mihonv1.ExtensionOrigin_StoreId{StoreId: "s0"}},
			Sources: []*mihonv1.ExtensionSource{{Id: "1001", Name: "Example Comics", Lang: "en", Available: true}}},
		{PkgName: "beispiel.comics", Name: "Beispiel Comics", VersionName: "2.0", VersionCode: 20,
			Sources: []*mihonv1.ExtensionSource{{Id: "2001", Name: "Beispiel Comics", Lang: "de", Available: true}}},
	}}
	stores := &fakeStores{stores: []*mihonv1.Store{{Id: "s0", Url: "https://store.example/repo.json", Name: "Existing", ExtensionCount: 3}}, admin: admin}
	mux := http.NewServeMux()
	path, h := mihonv1connect.NewMihonStoresServiceHandler(stores)
	mux.Handle(path, requireKey(h))
	apath, ah := mihonv1connect.NewMihonAdminServiceHandler(admin)
	mux.Handle(apath, requireKey(ah))
	mux.Handle("GET /v1/sources/{id}/icon", requireKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("icon-" + r.PathValue("id")))
	})))
	mux.Handle("/", plugin.NewHandler(p, key))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return &upstream{url: srv.URL, fake: f, stores: stores, admin: admin, srv: srv}
}

func requireKey(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.Header().Set("Content-Type", "application/grpc")
			w.Header().Set("Grpc-Status", "16")
			w.Header().Set("Grpc-Message", "bad key")
			w.WriteHeader(http.StatusOK)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func newPlugin(t *testing.T, up *upstream, extra map[string]any, managed bool) *mihon.Plugin {
	t.Helper()
	values := map[string]any{"mihon_source_url": up.url, "api_key": key}
	for k, v := range extra {
		values[k] = v
	}
	p, err := mihon.New(context.Background(), mihon.Options{Initial: values, Managed: managed, SyncEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestConformanceThroughThePlugin(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	rep := conformance.Run(t, client.InProcess(p), conformance.Options{
		Config: map[string]any{"mihon_source_url": up.url, "api_key": key},
	})
	if len(rep.Sources) != 2 || len(rep.Warnings) > 0 {
		t.Fatalf("report: %d sources, warnings %v", len(rep.Sources), rep.Warnings)
	}
	for _, s := range rep.Sources {
		if !s.OK() {
			t.Errorf("source %s: %+v", s.ID, s.Steps)
		}
	}
	// Every call reached mihon-source.
	if up.fake.Calls()["Search"] == 0 || up.fake.Calls()["FetchImage"] == 0 || up.fake.Calls()["ResolveUrl"] == 0 {
		t.Fatalf("calls: %v", up.fake.Calls())
	}
}

func TestConformanceRemoteOverGRPC(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	srv := httptest.NewUnstartedServer(plugin.NewHandler(p, "plugin-key"))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	conformance.Run(t, client.Remote(srv.URL, client.RemoteOptions{APIKey: "plugin-key"}), conformance.Options{})
}

// rateLimited fails Popular with a reason and a retry hint.
type rateLimited struct{ *plugintest.Fake }

func (rateLimited) Popular(context.Context, *sourcev1.PopularRequest) (*sourcev1.PopularResponse, error) {
	return nil, plugin.ErrRateLimited(9*time.Second, "slow down")
}

func TestSourceErrorsPassThrough(t *testing.T) {
	up := newUpstream(t, rateLimited{plugintest.New()})
	p := newPlugin(t, up, nil, false)
	_, err := p.Popular(context.Background(), &sourcev1.PopularRequest{SourceId: "1001", Page: 1})
	var pe *plugin.Error
	if !errors.As(err, &pe) || pe.Reason != plugin.ReasonRateLimited || pe.RetryAfter != 9*time.Second || pe.Code != plugin.CodeResourceExhausted {
		t.Fatalf("error: %#v", err)
	}
	_, err = p.GetSeries(context.Background(), &sourcev1.GetSeriesRequest{SourceId: "1001", Key: "/nope"})
	if plugin.ErrorReason(err) != plugin.ReasonNotFound {
		t.Fatalf("not found: %v", err)
	}
	if !strings.Contains(string(mustScrape(t, p)), `source_mihon_calls_total{code="resource_exhausted",method="Popular",reason="RATE_LIMITED"} 1`) {
		t.Fatalf("metrics:\n%s", mustScrape(t, p))
	}
}

func mustScrape(t *testing.T, p *mihon.Plugin) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	p.Metrics().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.Bytes()
}

func TestUnreachableMihonSource(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	up.srv.Close()
	started := time.Now()
	_, err := p.ListSources(context.Background(), &sourcev1.ListSourcesRequest{})
	if plugin.ErrorReason(err) != plugin.ReasonSourceDown || !strings.Contains(err.Error(), "can't be reached") {
		t.Fatalf("error: %v", err)
	}
	if time.Since(started) < time.Second {
		t.Fatalf("no retries: %s", time.Since(started))
	}
	h, _ := p.Health(context.Background(), &sourcev1.HealthRequest{})
	if h.GetStatus() != sourcev1.HealthStatus_HEALTH_STATUS_NOT_SERVING {
		t.Fatalf("health: %v", h)
	}
}

func TestConfig(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	ctx := context.Background()
	st, _ := structpb.NewStruct(map[string]any{"mihon_source_url": up.url, "api_key": "wrong"})
	if _, err := p.SetConfig(ctx, &sourcev1.SetConfigRequest{Values: st}); plugin.ErrorCode(err) != plugin.CodeInvalidArgument || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("wrong key: %v", err)
	}
	st, _ = structpb.NewStruct(map[string]any{"mihon_source_url": "ftp://nope"})
	if _, err := p.SetConfig(ctx, &sourcev1.SetConfigRequest{Values: st}); plugin.ErrorCode(err) != plugin.CodeInvalidArgument {
		t.Fatalf("bad URL: %v", err)
	}
	got, err := p.GetConfig(ctx, &sourcev1.GetConfigRequest{})
	if err != nil || got.GetValues().GetFields()["api_key"].GetStringValue() != "" || got.GetValues().GetFields()["mihon_source_url"].GetStringValue() != up.url {
		t.Fatalf("config: %v %v", got, err)
	}
	// The rejected changes left the working configuration in place.
	if p.Configured().APIKey != key {
		t.Fatal("the key was replaced by a rejected one")
	}
	info, _ := p.GetInfo(ctx, &sourcev1.GetInfoRequest{})
	if lim := info.GetInfo().GetLimits(); lim.GetImageRateLimit() != nil || lim.GetMaxImageConcurrencyPerSource() != 4 {
		t.Fatalf("image budget: %v", lim)
	}
	if info.GetInfo().GetConfigSchema() == nil || info.GetInfo().GetLimits().GetMaxConcurrencyPerSource() == 0 {
		t.Fatalf("info: %v", info)
	}
}

func TestNetworkProfilesAreForwarded(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	profile := &sourcev1.NetworkProfile{Name: "vpn", Proxy: &sourcev1.Proxy{Url: "http://gluetun:8888"}}
	if _, err := p.SetNetworkProfile(context.Background(), &sourcev1.SetNetworkProfileRequest{SourceId: "1001", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	if up.fake.Profile("1001").GetProxy().GetUrl() != "http://gluetun:8888" {
		t.Fatalf("profile: %v", up.fake.Profile("1001"))
	}
}

func TestStoresAndExtensionsAreKept(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	up.stores.available = []*mihonv1.AvailableExtension{
		{StoreId: "s0", PkgName: "example.ext.one", VersionCode: 3, Supported: true},
		{StoreId: "s1", PkgName: "example.ext.one", VersionCode: 2, Supported: true, HasJar: true},
		{StoreId: "s0", PkgName: "example.ext.two", Installed: true, Supported: true},
		{StoreId: "s0", PkgName: "example.ext.old", Supported: false},
	}
	before := time.Now()
	p := newPlugin(t, up, map[string]any{
		"stores": []any{
			map[string]any{"url": "https://store.example/repo.json/"}, // already there
			map[string]any{"url": "https://other.example/repo.json", "signing_key_fingerprint": "ab12"},
		},
		"extensions": []any{"example.ext.one", "example.ext.two", "example.ext.old"},
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Synced(ctx, before); err != nil {
		t.Fatal(err)
	}
	up.stores.mu.Lock()
	added, installs := up.stores.added, up.stores.installs
	up.stores.mu.Unlock()
	if len(added) != 1 || added[0] != "https://other.example/repo.json|ab12" {
		t.Fatalf("added: %v", added)
	}
	// The jar is preferred; installed ones are left alone.
	if len(installs) != 1 || installs[0] != "s1/example.ext.one" {
		t.Fatalf("installs: %v", installs)
	}
	if !strings.Contains(p.StoreProblem(), "example.ext.old: no store offers a supported version") {
		t.Fatalf("problem: %q", p.StoreProblem())
	}
	h, _ := p.Health(context.Background(), &sourcev1.HealthRequest{})
	if h.GetStatus() != sourcev1.HealthStatus_HEALTH_STATUS_DEGRADED || !strings.Contains(h.GetMessage(), "example.ext.old") {
		t.Fatalf("health: %v", h)
	}

	// A configuration change reconciles again; nothing is added twice.
	again := time.Now()
	st, _ := structpb.NewStruct(map[string]any{"mihon_source_url": up.url, "api_key": "",
		"stores": []any{map[string]any{"url": "https://other.example/repo.json"}}, "extensions": []any{"example.ext.one"}})
	if _, err := p.SetConfig(context.Background(), &sourcev1.SetConfigRequest{Values: st}); err != nil {
		t.Fatal(err)
	}
	if err := p.Synced(ctx, again); err != nil {
		t.Fatal(err)
	}
	up.stores.mu.Lock()
	defer up.stores.mu.Unlock()
	if len(up.stores.added) != 1 || len(up.stores.installs) != 1 || p.StoreProblem() != "" {
		t.Fatalf("second round: added %v installs %v problem %q", up.stores.added, up.stores.installs, p.StoreProblem())
	}
}

func TestIcons(t *testing.T) {
	f := plugintest.New()
	f.Sources[0].Source.IconUrl = "/v1/sources/1001/icon"
	up := newUpstream(t, f)

	standalone := newPlugin(t, up, nil, false)
	res, _ := standalone.ListSources(context.Background(), &sourcev1.ListSourcesRequest{})
	if res.GetSources()[0].GetIconUrl() != "/v1/sources/1001/icon" {
		t.Fatalf("standalone icon: %q", res.GetSources()[0].GetIconUrl())
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/sources/{sourceId}/icon", standalone.IconHandler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sources/1001/icon", nil))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || !bytes.Equal(body, []byte("icon-1001")) || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon: %d %q", rec.Code, body)
	}

	managed := newPlugin(t, up, nil, true)
	res, _ = managed.ListSources(context.Background(), &sourcev1.ListSourcesRequest{})
	if res.GetSources()[0].GetIconUrl() != "" {
		t.Fatalf("managed mode kept a relative icon path: %q", res.GetSources()[0].GetIconUrl())
	}
}

// storeError is an error as mihon-source's store calls answer it.
func storeError(code connect.Code, reason, msg string) error {
	e := connect.NewError(code, errors.New(msg))
	if reason != "" {
		if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: "mihon.panelarr"}); err == nil {
			e.AddDetail(d)
		}
	}
	return e
}

func TestStoreProblemsNameTheStoreNotMihonSource(t *testing.T) {
	const store = "https://other.example/repo.json"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"fetch failed", storeError(connect.CodeUnavailable, "STORE_FETCH_FAILED", "GET "+store+" failed: connection refused"),
			"store " + store + " couldn't be fetched: GET " + store + " failed: connection refused"},
		{"older mihon-source without reasons", storeError(connect.CodeUnavailable, "", "GET "+store+" failed with HTTP 404"),
			"store " + store + " couldn't be fetched: GET " + store + " failed with HTTP 404"},
		{"invalid store", storeError(connect.CodeInvalidArgument, "STORE_INVALID", "the store announces signing key 'ab', not the expected one"),
			"store " + store + " can't be used: the store announces signing key 'ab', not the expected one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := newUpstream(t, plugintest.New())
			up.stores.addErr = c.err
			before := time.Now()
			p := newPlugin(t, up, map[string]any{"stores": []any{map[string]any{"url": store}}}, false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Synced(ctx, before); err != nil {
				t.Fatal(err)
			}
			if got := p.StoreProblem(); got != c.want {
				t.Fatalf("problem:\n got %q\nwant %q", got, c.want)
			}
			h, _ := p.Health(context.Background(), &sourcev1.HealthRequest{})
			if h.GetStatus() != sourcev1.HealthStatus_HEALTH_STATUS_DEGRADED || strings.Contains(h.GetMessage(), "can't be reached") {
				t.Fatalf("health: %v", h)
			}
			// mihon-source answered at once: no retries as if it were down.
			if time.Since(before) > time.Second {
				t.Fatalf("took %s", time.Since(before))
			}
		})
	}
}

func TestStoreSyncWithMihonSourceDown(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	up.srv.Close()
	before := time.Now()
	p := newPlugin(t, up, map[string]any{"stores": []any{map[string]any{"url": "https://other.example/repo.json"}}}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Synced(ctx, before); err != nil {
		t.Fatal(err)
	}
	if got := p.StoreProblem(); !strings.HasPrefix(got, "listing stores: ") || !strings.Contains(got, "mihon-source at "+up.url+" can't be reached") {
		t.Fatalf("problem: %q", got)
	}
}
