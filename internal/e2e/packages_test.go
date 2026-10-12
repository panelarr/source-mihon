// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/client"
	"github.com/panelarr/source-api/sdk/conformance"
	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/internal/mihon"
)

const testPkg = "panelarr.testext.lib16"

// TestEndToEndPackages installs, narrows, updates and removes the test
// extension from mihon-source's fixture store through the package calls.
func TestEndToEndPackages(t *testing.T) {
	bin, storeDir := os.Getenv("SOURCE_MIHON_E2E_MIHON_SOURCE"), os.Getenv("SOURCE_MIHON_E2E_STORE")
	if bin == "" || storeDir == "" {
		t.Skip("run with make e2e (needs a mihon-source build and its fixture store)")
	}
	fingerprint, err := os.ReadFile(filepath.Join(storeDir, "FINGERPRINT")) //nolint:gosec // test input
	if err != nil {
		t.Fatal(err)
	}
	// The store: v1 first, v2 (a newer version) later, at the same URL.
	var version atomic.Value
	version.Store("v1")
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(storeDir, version.Load().(string), filepath.Clean(r.URL.Path)))
	}))
	defer store.Close()
	web := site()
	defer web.Close()
	mihonURL := startMihonSource(t, bin, web.URL)
	storeURL := store.URL + "/store/repo.json"

	ctx := context.Background()
	before := time.Now()
	p, err := mihon.New(ctx, mihon.Options{Initial: map[string]any{
		"mihon_source_url": mihonURL, "api_key": adminToken,
		"stores": []any{map[string]any{"url": storeURL, "signing_key_fingerprint": strings.TrimSpace(string(fingerprint))}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := p.Synced(syncCtx, before); err != nil || p.StoreProblem() != "" {
		t.Fatalf("store sync: %v %q", err, p.StoreProblem())
	}
	srv := httptest.NewUnstartedServer(plugin.NewHandler(p, "plugin-key"))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	c := client.Remote(srv.URL, client.RemoteOptions{APIKey: "plugin-key"})

	// The conformance walk: install, again, sources, one language, restore, uninstall.
	rep := conformance.Run(t, c, conformance.Options{PackageID: testPkg, Timeout: 3 * time.Minute})
	for _, st := range rep.PackageSteps {
		t.Logf("  %-20s ok=%-5v %s", st.Name, st.OK, st.Detail)
		if !st.OK {
			t.Errorf("package step %s failed: %s", st.Name, st.Detail)
		}
	}
	if len(rep.PackageSteps) != 6 {
		t.Fatalf("package steps: %+v", rep.PackageSteps)
	}

	// Install for real, then the plugin's own languages narrow the sources.
	inst, err := c.InstallPackage(ctx, &sourcev1.InstallPackageRequest{PackageId: testPkg})
	if err != nil || inst.GetPackage().GetInstalledVersion() != "1.6.2" {
		t.Fatalf("install: %v %v", inst, err)
	}
	langsOf := func() []string {
		l, err := c.ListSources(ctx, &sourcev1.ListSourcesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range l.GetSources() {
			if s.GetPackageId() != testPkg {
				t.Errorf("source %s names package %q", s.GetId(), s.GetPackageId())
			}
			out = append(out, s.GetLang())
		}
		return out
	}
	if got := strings.Join(langsOf(), ","); got != "en,fr" && got != "fr,en" {
		t.Fatalf("installed sources: %s", got)
	}
	cfg, _ := structpb.NewStruct(map[string]any{"mihon_source_url": mihonURL, "languages": []any{"fr"}})
	if _, err := c.SetConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(langsOf(), ","); got != "fr" {
		t.Fatalf("with languages [fr]: %s", got)
	}
	cfg, _ = structpb.NewStruct(map[string]any{"mihon_source_url": mihonURL})
	if _, err := c.SetConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// A newer version in the store: refresh it in mihon-source, then update.
	version.Store("v2")
	refreshStores(t, mihonURL)
	updates, err := c.ListPackages(ctx, &sourcev1.ListPackagesRequest{UpdatesOnly: true})
	if err != nil || len(updates.GetPackages()) != 1 || updates.GetPackages()[0].GetVersion() != "1.6.3" {
		t.Fatalf("updates: %v %v", updates, err)
	}
	for range 2 { // again is a no-op
		up, err := c.UpdatePackage(ctx, &sourcev1.UpdatePackageRequest{PackageId: testPkg})
		if err != nil || up.GetPackage().GetInstalledVersion() != "1.6.3" || up.GetPackage().GetUpdateAvailable() {
			t.Fatalf("update: %v %v", up, err)
		}
	}
	if _, err := c.UninstallPackage(ctx, &sourcev1.UninstallPackageRequest{PackageId: testPkg}); err != nil {
		t.Fatal(err)
	}
	if got := langsOf(); len(got) != 0 {
		t.Fatalf("sources after uninstall: %v", got)
	}
}

// refreshStores asks mihon-source to download its stores' indexes again.
func refreshStores(t *testing.T, mihonURL string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, mihonURL+"/v1/admin/jobs/store-refresh:run", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("store refresh: HTTP %d", res.StatusCode)
	}
	// The job runs in the background; wait until the index says 1.6.3.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, mihonURL+"/v1/admin/stores/available?updatesOnly=true", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		if res, err := http.DefaultClient.Do(req); err == nil {
			var b strings.Builder
			buf := make([]byte, 4096)
			for {
				n, err := res.Body.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			_ = res.Body.Close()
			if strings.Contains(b.String(), "1.6.3") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the store refresh didn't show 1.6.3")
}
