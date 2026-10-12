// SPDX-License-Identifier: GPL-3.0-or-later

package mihon_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"
	"github.com/panelarr/source-api/sdk/plugintest"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// offers: a multi-language package from two stores (s1 has the newer jar),
// an update for an installed one, and an unsupported one.
func withOffers(up *upstream) {
	up.stores.stores = append(up.stores.stores, &mihonv1.Store{Id: "s1", Url: "https://other.example/repo.json", Name: "Other"})
	multi := func(store string, code int64, jar bool) *mihonv1.AvailableExtension {
		return &mihonv1.AvailableExtension{
			StoreId: store, PkgName: "multi.comics", Name: "Multi Comics", VersionName: "3." + string(rune('0'+code%10)), VersionCode: code,
			Supported: true, HasJar: jar, ContentWarning: mihonv1.ContentWarning_CONTENT_WARNING_MIXED,
			Sources: []*mihonv1.AvailableSource{{Id: "3001", Name: "Multi", Lang: "en"}, {Id: "3002", Name: "Multi", Lang: "fr"}, {Id: "3003", Name: "Multi", Lang: "all"}},
		}
	}
	up.stores.available = []*mihonv1.AvailableExtension{
		multi("s0", 30, false),
		multi("s1", 31, true),
		{StoreId: "s0", PkgName: "example.comics", Name: "Example Comics", VersionName: "1.1", VersionCode: 11, Supported: true, Installed: true, UpdateAvailable: true,
			Sources: []*mihonv1.AvailableSource{{Id: "1001", Name: "Example Comics", Lang: "en"}}},
		{StoreId: "s0", PkgName: "old.comics", Name: "Old Comics", VersionName: "0.1", VersionCode: 1, Supported: false,
			Sources: []*mihonv1.AvailableSource{{Id: "4001", Name: "Old", Lang: "en"}}},
	}
}

func pkgs(t *testing.T, list *sourcev1.ListPackagesResponse) map[string]*sourcev1.Package {
	t.Helper()
	out := map[string]*sourcev1.Package{}
	for _, p := range list.GetPackages() {
		out[p.GetId()] = p
	}
	return out
}

func TestPackagesCombineStoresAndInstalledExtensions(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	withOffers(up)
	p := newPlugin(t, up, map[string]any{"languages": []any{"en", "de", "all"}}, false)
	ctx := context.Background()
	list, err := p.ListPackages(ctx, &sourcev1.ListPackagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	all := pkgs(t, list)
	if len(all) != 4 {
		t.Fatalf("packages: %v", all)
	}
	m := all["multi.comics"]
	if m.GetInstalled() || m.GetVersion() != "3.1" || len(m.GetOffers()) != 2 || m.GetOffers()[1].GetOriginName() != "Other" ||
		m.GetContentRating() != sourcev1.ContentRating_CONTENT_RATING_MIXED || !m.GetSupported() {
		t.Fatalf("multi: %v", m)
	}
	avail := map[string]bool{}
	for _, s := range m.GetSources() {
		avail[s.GetLang()] = s.GetAvailable()
	}
	if !avail["en"] || avail["fr"] || !avail["all"] {
		t.Fatalf("multi languages: %v", avail)
	}
	e := all["example.comics"]
	if !e.GetInstalled() || e.GetInstalledVersion() != "1.0" || !e.GetUpdateAvailable() || e.GetIconUrl() != "/v1/packages/example.comics/icon" {
		t.Fatalf("example: %v", e)
	}
	if all["old.comics"].GetSupported() {
		t.Fatal("an unsupported offer counted as supported")
	}
	// Filters.
	for req, want := range map[*sourcev1.ListPackagesRequest]int{
		{InstalledOnly: true}: 2, {UpdatesOnly: true}: 1, {Lang: "fr"}: 1, {OriginId: "s1"}: 1, {Query: "multi"}: 1,
	} {
		got, _ := p.ListPackages(ctx, req)
		if len(got.GetPackages()) != want {
			t.Errorf("filter %v: %d packages", req, len(got.GetPackages()))
		}
	}
	if _, err := p.GetPackage(ctx, &sourcev1.GetPackageRequest{PackageId: "nope"}); plugin.ErrorCode(err) != plugin.CodeNotFound {
		t.Fatalf("unknown package: %v", err)
	}
}

func TestInstallUpdateUninstall(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	withOffers(up)
	p := newPlugin(t, up, nil, false)
	ctx := context.Background()

	// The best offer: s1's newer jar. Again: nothing to do.
	for range 2 {
		res, err := p.InstallPackage(ctx, &sourcev1.InstallPackageRequest{PackageId: "multi.comics"})
		if err != nil || !res.GetPackage().GetInstalled() || res.GetPackage().GetInstalledVersion() != "3.1" {
			t.Fatalf("install: %v %v", res, err)
		}
	}
	if strings.Join(up.stores.installs, ",") != "s1/multi.comics" {
		t.Fatalf("installs: %v", up.stores.installs)
	}
	// Another origin's (older) version is a change.
	if _, err := p.InstallPackage(ctx, &sourcev1.InstallPackageRequest{PackageId: "multi.comics", OriginId: "s0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.InstallPackage(ctx, &sourcev1.InstallPackageRequest{PackageId: "multi.comics", OriginId: "elsewhere"}); plugin.ErrorCode(err) != plugin.CodeInvalidArgument {
		t.Fatalf("unknown origin: %v", err)
	}
	if _, err := p.InstallPackage(ctx, &sourcev1.InstallPackageRequest{PackageId: "old.comics"}); plugin.ErrorCode(err) != plugin.CodeFailedPrecondition {
		t.Fatalf("unsupported: %v", err)
	}

	// Update: to the newest; up to date is a no-op; not installed fails.
	before := len(up.stores.installs)
	u, err := p.UpdatePackage(ctx, &sourcev1.UpdatePackageRequest{PackageId: "multi.comics"})
	if err != nil || u.GetPackage().GetInstalledVersion() != "3.1" || len(up.stores.installs) != before+1 {
		t.Fatalf("update: %v %v %v", u, err, up.stores.installs)
	}
	if _, err := p.UpdatePackage(ctx, &sourcev1.UpdatePackageRequest{PackageId: "multi.comics"}); err != nil || len(up.stores.installs) != before+1 {
		t.Fatalf("update again: %v %v", err, up.stores.installs)
	}
	if _, err := p.UpdatePackage(ctx, &sourcev1.UpdatePackageRequest{PackageId: "old.comics"}); plugin.ErrorCode(err) != plugin.CodeFailedPrecondition {
		t.Fatalf("update of a missing package: %v", err)
	}

	// Languages go to mihon-source.
	if _, err := p.SetPackageSettings(ctx, &sourcev1.SetPackageSettingsRequest{PackageId: "multi.comics", Settings: &sourcev1.PackageSettings{EnabledLangs: []string{"en"}}}); err != nil {
		t.Fatal(err)
	}
	if got := up.admin.langs["multi.comics"]; len(got) != 1 || got[0] != "en" {
		t.Fatalf("languages: %v", up.admin.langs)
	}

	// Uninstall, and again: a no-op.
	for range 2 {
		if _, err := p.UninstallPackage(ctx, &sourcev1.UninstallPackageRequest{PackageId: "multi.comics"}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(up.admin.uninstalls, ",") != "multi.comics" {
		t.Fatalf("uninstalls: %v", up.admin.uninstalls)
	}
}

func TestStoreFailuresOnInstall(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	withOffers(up)
	p := newPlugin(t, up, nil, false)
	for reason, code := range map[string]plugin.Code{"STORE_FETCH_FAILED": plugin.CodeUnavailable, "STORE_INVALID": plugin.CodeFailedPrecondition} {
		mihonCode := connect.CodeUnavailable
		if reason == "STORE_INVALID" {
			mihonCode = connect.CodeInvalidArgument
		}
		up.stores.installErr = storeError(mihonCode, reason, "the jar couldn't be fetched")
		_, err := p.InstallPackage(context.Background(), &sourcev1.InstallPackageRequest{PackageId: "multi.comics"})
		if plugin.ErrorCode(err) != code || string(plugin.ErrorReason(err)) != reason || strings.Contains(err.Error(), "can't be reached") {
			t.Errorf("%s: %v", reason, err)
		}
	}
}

func TestPluginLanguagesFilterSources(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, map[string]any{"languages": []any{"en"}}, false)
	ctx := context.Background()
	list, err := p.ListSources(ctx, &sourcev1.ListSourcesRequest{})
	if err != nil || len(list.GetSources()) != 1 || list.GetSources()[0].GetId() != "1001" || list.GetSources()[0].GetPackageId() != "example.comics" {
		t.Fatalf("sources: %v %v", list, err)
	}
	if _, err := p.GetSource(ctx, &sourcev1.GetSourceRequest{SourceId: "2001"}); plugin.ErrorCode(err) != plugin.CodeNotFound {
		t.Fatalf("hidden source: %v", err)
	}
	if _, err := p.Popular(ctx, &sourcev1.PopularRequest{SourceId: "2001", Page: 1}); plugin.ErrorCode(err) != plugin.CodeNotFound {
		t.Fatalf("browsing a hidden source: %v", err)
	}
	if _, err := p.Popular(ctx, &sourcev1.PopularRequest{SourceId: "1001", Page: 1}); err != nil {
		t.Fatalf("an offered source: %v", err)
	}
	src, err := p.GetSource(ctx, &sourcev1.GetSourceRequest{SourceId: "1001"})
	if err != nil || src.GetSource().GetPackageId() != "example.comics" {
		t.Fatalf("package id: %v %v", src, err)
	}
}

func TestOriginsDetailsAndReload(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	withOffers(up)
	p := newPlugin(t, up, nil, false)
	ctx := context.Background()
	origins, err := p.ListOrigins(ctx, &sourcev1.ListOriginsRequest{})
	if err != nil || len(origins.GetOrigins()) != 2 {
		t.Fatalf("origins: %v, %v", origins, err)
	}
	if o := origins.GetOrigins()[0]; o.GetId() != "s0" || o.GetName() != "Existing" || o.GetPackageCount() != 3 || o.GetUrl() == "" {
		t.Fatalf("origin: %v", o)
	}
	refreshed, err := p.RefreshOrigin(ctx, &sourcev1.RefreshOriginRequest{OriginId: "s1"})
	if err != nil || refreshed.GetOrigin().GetRefreshedAt() == nil {
		t.Fatalf("refresh: %v, %v", refreshed, err)
	}
	up.stores.refreshErr = storeError(connect.CodeUnavailable, "STORE_FETCH_FAILED", "GET store failed")
	_, err = p.RefreshOrigin(ctx, &sourcev1.RefreshOriginRequest{OriginId: "s1"})
	if plugin.ErrorReason(err) != plugin.ReasonStoreFetchFailed {
		t.Fatalf("failing refresh: %v", err)
	}
	if _, err := p.RefreshOrigin(ctx, &sourcev1.RefreshOriginRequest{OriginId: "nope"}); plugin.ErrorCode(err) != plugin.CodeNotFound {
		t.Fatalf("unknown origin: %v", err)
	}

	got, err := p.ReloadPackage(ctx, &sourcev1.ReloadPackageRequest{PackageId: "example.comics"})
	if err != nil {
		t.Fatal(err)
	}
	d := got.GetPackage().GetDetails()
	if d.GetApiVersion() != "1.5" || d.GetSigningFingerprint() != "ab12" || d.GetOriginId() != "s0" || strings.Join(up.admin.reloads, ",") != "example.comics" {
		t.Fatalf("reload: %v, reloads %v", got, up.admin.reloads)
	}
	if _, err := p.ReloadPackage(ctx, &sourcev1.ReloadPackageRequest{PackageId: "multi.comics"}); plugin.ErrorCode(err) != plugin.CodeFailedPrecondition {
		t.Fatalf("reload a package that isn't installed: %v", err)
	}
	list, _ := p.ListPackages(ctx, &sourcev1.ListPackagesRequest{})
	if pkgs(t, list)["multi.comics"].GetDetails() != nil {
		t.Fatal("details on a package that isn't installed")
	}
}
