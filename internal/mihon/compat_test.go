// SPDX-License-Identifier: GPL-3.0-or-later

package mihon_test

import (
	"context"
	"slices"
	"testing"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugintest"
)

// Compat IDs let a host recognise a source by the ID another app gave it
// (a backup's source ID, for example). mihon-source sets them on its sources
// and on the sources of installed and offered extensions; source-mihon must
// keep them on every path.

func TestCompatIDsOnSources(t *testing.T) {
	f := plugintest.New()
	f.Sources[0].Source.CompatIds = []string{"mihon:1001"}
	f.Sources[1].Source.CompatIds = nil
	p := newPlugin(t, newUpstream(t, f), nil, false)
	ctx := context.Background()

	list, err := p.ListSources(ctx, &sourcev1.ListSourcesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, s := range list.GetSources() {
		got[s.GetId()] = s.GetCompatIds()
	}
	if !slices.Equal(got["1001"], []string{"mihon:1001"}) || len(got["2001"]) != 0 {
		t.Fatalf("ListSources compat IDs: %v", got)
	}

	one, err := p.GetSource(ctx, &sourcev1.GetSourceRequest{SourceId: "1001"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one.GetSource().GetCompatIds(), []string{"mihon:1001"}) {
		t.Fatalf("GetSource compat IDs: %v", one.GetSource().GetCompatIds())
	}
}

func TestCompatIDsOnPackageSources(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	withOffers(up)
	// An installed extension's sources, and those of one that is only
	// offered. The installed one's store offer has none, so its IDs can only
	// come from the installed extension.
	up.admin.installed[0].Sources[0].CompatIds = []string{"mihon:1001"}
	up.admin.installed[1].Sources[0].CompatIds = nil
	for _, a := range up.stores.available {
		if a.GetPkgName() != "multi.comics" {
			continue
		}
		for _, s := range a.GetSources() {
			s.CompatIds = []string{"mihon:" + s.GetId()}
		}
	}
	p := newPlugin(t, up, map[string]any{"languages": []any{"en", "de", "fr", "all"}}, false)

	list, err := p.ListPackages(context.Background(), &sourcev1.ListPackagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	all := pkgs(t, list)
	ids := func(pkg string) map[string][]string {
		out := map[string][]string{}
		for _, s := range all[pkg].GetSources() {
			out[s.GetId()] = s.GetCompatIds()
		}
		return out
	}
	// Installed: taken from the installed extension.
	if got := ids("example.comics"); !slices.Equal(got["1001"], []string{"mihon:1001"}) {
		t.Fatalf("installed package: %v", got)
	}
	// Installed without compat IDs stays without.
	if got := ids("beispiel.comics"); len(got["2001"]) != 0 {
		t.Fatalf("installed package without compat IDs: %v", got)
	}
	// Only offered: taken from the store listing.
	got := ids("multi.comics")
	for _, id := range []string{"3001", "3002", "3003"} {
		if !slices.Equal(got[id], []string{"mihon:" + id}) {
			t.Fatalf("store listing: %v", got)
		}
	}
}
