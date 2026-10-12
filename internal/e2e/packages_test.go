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

	jobsv1 "github.com/panelarr/source-api/gen/panelarr/jobs/v1"
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
	if len(rep.PackageSteps) != 7 {
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
	checkOriginsDetailsAndReload(ctx, t, c, storeURL)
	checkJobsAndHealthChecks(ctx, t, p, c, mihonURL)
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
	origins, err := c.ListOrigins(ctx, &sourcev1.ListOriginsRequest{})
	if err != nil || len(origins.GetOrigins()) != 1 {
		t.Fatalf("origins: %v %v", origins, err)
	}
	if _, err := c.RefreshOrigin(ctx, &sourcev1.RefreshOriginRequest{OriginId: origins.GetOrigins()[0].GetId()}); err != nil {
		t.Fatal(err)
	}
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

// checkOriginsDetailsAndReload: the store as an origin, the installed
// extension's details, and a reload.
func checkOriginsDetailsAndReload(ctx context.Context, t *testing.T, c client.Client, storeURL string) {
	t.Helper()
	origins, err := c.ListOrigins(ctx, &sourcev1.ListOriginsRequest{})
	if err != nil || len(origins.GetOrigins()) != 1 {
		t.Fatalf("origins: %v %v", origins, err)
	}
	o := origins.GetOrigins()[0]
	if o.GetUrl() != storeURL || o.GetPackageCount() != 1 || o.GetSigningKeyFingerprint() == "" || o.GetRefreshedAt() == nil {
		t.Fatalf("origin: %v", o)
	}
	reloaded, err := c.ReloadPackage(ctx, &sourcev1.ReloadPackageRequest{PackageId: testPkg})
	if err != nil {
		t.Fatal(err)
	}
	d := reloaded.GetPackage().GetDetails()
	if d.GetApiVersion() != "1.6" || d.GetOriginId() != o.GetId() || d.GetSigningFingerprint() == "" || d.GetSha256() == "" || d.GetInstalledAt() == nil {
		t.Fatalf("details: %v", d)
	}
}

// checkJobsAndHealthChecks: mihon-source's jobs through the plugin, a check
// of one source with its steps noted, the per-source switch, and the
// health-check schedule from the plugin's configuration.
func checkJobsAndHealthChecks(ctx context.Context, t *testing.T, p *mihon.Plugin, c client.Client, mihonURL string) {
	t.Helper()
	list, err := c.ListJobs(ctx, &sourcev1.ListJobsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range list.GetJobs() {
		ids = append(ids, j.GetId())
	}
	if got := strings.Join(ids, ","); !strings.HasPrefix(got, "plugin.sync,") || !strings.Contains(got, "source-health") || !strings.Contains(got, "store-refresh") {
		t.Fatalf("jobs: %s", got)
	}
	sources, err := c.ListSources(ctx, &sourcev1.ListSourcesRequest{Lang: "en"})
	if err != nil || len(sources.GetSources()) == 0 {
		t.Fatalf("sources: %v %v", sources, err)
	}
	src := sources.GetSources()[0].GetId()
	run, err := c.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "source-health", Params: map[string]string{"source_ids": src, "steps": "details"}})
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for deadline := time.Now().Add(time.Minute); ; {
		got, err := c.GetJobRun(ctx, &sourcev1.GetJobRunRequest{RunId: run.GetRun().GetId()})
		if err != nil {
			t.Fatal(err)
		}
		if got.GetRun().GetSummary().GetStatus() != jobsv1.JobRunStatus_JOB_RUN_STATUS_RUNNING {
			for _, n := range got.GetRun().GetNotes() {
				notes = append(notes, n.GetMessage())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the check didn't finish")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if all := strings.Join(notes, "\n"); !strings.Contains(all, "popular: ok") || !strings.Contains(all, "details: ok") {
		t.Fatalf("check notes: %s", all)
	}

	off, err := c.SetSourceHealthCheck(ctx, &sourcev1.SetSourceHealthCheckRequest{SourceId: src})
	if err != nil || !off.GetSource().GetHealth().GetChecksDisabled() {
		t.Fatalf("checks off: %v %v", off, err)
	}
	if on, err := c.SetSourceHealthCheck(ctx, &sourcev1.SetSourceHealthCheckRequest{SourceId: src, Enabled: true}); err != nil || on.GetSource().GetHealth().GetChecksDisabled() {
		t.Fatalf("checks on: %v %v", on, err)
	}

	before := time.Now()
	cfg, _ := structpb.NewStruct(map[string]any{
		"mihon_source_url": mihonURL, "languages": []any{"en"},
		"health_checks": map[string]any{"interval": "6h", "sample": 2},
	})
	if _, err := c.SetConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := p.Synced(syncCtx, before); err != nil || p.StoreProblem() != "" {
		t.Fatalf("sync: %v %q", err, p.StoreProblem())
	}
	job, err := c.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "source-health"})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for _, prm := range job.GetJob().GetParams() {
		params[prm.GetName()] = prm.GetDefault()
	}
	if s := job.GetJob().GetSchedule(); s.GetInterval().AsDuration() != 6*time.Hour || s.GetSource() != "SetHealthCheckSchedule" || params["langs"] != "en" || params["sample"] != "2" {
		t.Fatalf("source-health job: %v", job.GetJob())
	}
}
