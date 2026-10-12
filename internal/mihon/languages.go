// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// catalogueTTL is how long the source languages and packages learned from
// mihon-source are trusted before asking again.
const catalogueTTL = 30 * time.Second

// catalogue remembers each source's language and package, for the language
// check on calls to one source and for Source.package_id. mihon-source
// already hides sources outside an extension's languages; the plugin's own
// languages are applied here.
type catalogue struct {
	mu       sync.Mutex
	langs    map[string]string // source ID → language
	packages map[string]string // source ID → package
	at       time.Time
}

func (c *catalogue) put(sources []*sourcev1.Source, packages map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.langs = map[string]string{}
	for _, s := range sources {
		c.langs[s.GetId()] = s.GetLang()
	}
	if packages != nil {
		c.packages = packages
	}
	c.at = time.Now()
}

// invalidate makes the next lookup ask mihon-source again.
func (c *catalogue) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

func (c *catalogue) lookup(id string) (lang, pkg string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) > catalogueTTL {
		return "", "", false
	}
	lang, ok = c.langs[id]
	return lang, c.packages[id], ok
}

// sourcePackages maps every installed source to its extension, or nil when
// mihon-source won't tell (e.g. the key has no admin scope).
func (p *Plugin) sourcePackages(ctx context.Context, u *upstream) map[string]string {
	res, err := call(ctx, u, func() (*connect.Response[mihonv1.ListExtensionsResponse], error) {
		return u.extensions.ListExtensions(ctx, connect.NewRequest(&mihonv1.ListExtensionsRequest{}))
	})
	if err != nil {
		p.log.Debug("can't list extensions for package IDs", "error", err)
		return nil
	}
	out := map[string]string{}
	for _, e := range res.Msg.GetExtensions() {
		for _, s := range e.GetSources() {
			out[s.GetId()] = e.GetPkgName()
		}
	}
	return out
}

// refreshCatalogue lists mihon-source's sources and remembers their languages
// and packages. It returns them unfiltered.
func (p *Plugin) refreshCatalogue(ctx context.Context, u *upstream, lang string) ([]*sourcev1.Source, error) {
	res, err := call(ctx, u, func() (*sourcev1.ListSourcesResponse, error) {
		return u.source.ListSources(ctx, &sourcev1.ListSourcesRequest{Lang: lang})
	})
	if err != nil {
		return nil, err
	}
	pkgs := p.sourcePackages(ctx, u)
	if lang == "" {
		p.cat.put(res.GetSources(), pkgs)
	}
	for _, s := range res.GetSources() {
		s.PackageId = pkgs[s.GetId()]
	}
	return res.GetSources(), nil
}

// offered fails with NOT_FOUND when a source's language isn't among the
// plugin's languages, so sources outside them can't be browsed or searched.
func (p *Plugin) offered(ctx context.Context, sourceID string) error {
	set := p.settings.Load()
	if set == nil || len(set.Langs) == 0 {
		return nil
	}
	lang, _, ok := p.cat.lookup(sourceID)
	if !ok {
		u, err := p.upstream()
		if err != nil {
			return err
		}
		if _, err := p.refreshCatalogue(ctx, u, ""); err != nil {
			return nil // let the call itself report it
		}
		if lang, _, ok = p.cat.lookup(sourceID); !ok {
			return nil // mihon-source doesn't list it; it will say NOT_FOUND
		}
	}
	if !set.offers(lang) {
		return plugin.ErrNotFound("source %s isn't offered: its language %q isn't among the plugin's languages", sourceID, lang)
	}
	return nil
}
