// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// The package calls: mihon-source's extensions as source API packages. What
// the stores offer and what is installed are put together per package name;
// installs, updates and removals go through mihon-source's store and
// extension APIs, which verify signatures and keep installs all-or-nothing.

// view is what source-mihon knows about one package.
type view struct {
	id        string
	offers    []*mihonv1.AvailableExtension
	installed *mihonv1.Extension
}

// packageWorld is mihon-source's stores, offers and installed extensions.
type packageWorld struct {
	storeNames map[string]string
	packages   map[string]*view
}

func (p *Plugin) world(ctx context.Context, u *upstream) (*packageWorld, error) {
	stores, err := call(ctx, u, func() (*connect.Response[mihonv1.ListStoresResponse], error) {
		return u.stores.ListStores(ctx, connect.NewRequest(&mihonv1.ListStoresRequest{}))
	})
	if err != nil {
		return nil, err
	}
	avail, err := call(ctx, u, func() (*connect.Response[mihonv1.ListAvailableResponse], error) {
		return u.stores.ListAvailable(ctx, connect.NewRequest(&mihonv1.ListAvailableRequest{}))
	})
	if err != nil {
		return nil, err
	}
	exts, err := call(ctx, u, func() (*connect.Response[mihonv1.ListExtensionsResponse], error) {
		return u.extensions.ListExtensions(ctx, connect.NewRequest(&mihonv1.ListExtensionsRequest{}))
	})
	if err != nil {
		return nil, err
	}
	w := &packageWorld{storeNames: map[string]string{}, packages: map[string]*view{}}
	for _, st := range stores.Msg.GetStores() {
		w.storeNames[st.GetId()] = st.GetName()
	}
	get := func(id string) *view {
		v, ok := w.packages[id]
		if !ok {
			v = &view{id: id}
			w.packages[id] = v
		}
		return v
	}
	for _, a := range avail.Msg.GetExtensions() {
		if plugin.PackageIDPattern.MatchString(a.GetPkgName()) {
			get(a.GetPkgName()).offers = append(get(a.GetPkgName()).offers, a)
		}
	}
	for _, e := range exts.Msg.GetExtensions() {
		if plugin.PackageIDPattern.MatchString(e.GetPkgName()) {
			get(e.GetPkgName()).installed = e
		}
	}
	return w, nil
}

// best is the offer to install: a supported one, preferring a jar, then the
// newest.
func (v *view) best() *mihonv1.AvailableExtension {
	return pickOffer(v.offers)
}

// newest is the newest offer at all, for the version shown.
func (v *view) newest() *mihonv1.AvailableExtension {
	var n *mihonv1.AvailableExtension
	for _, o := range v.offers {
		if n == nil || o.GetVersionCode() > n.GetVersionCode() {
			n = o
		}
	}
	return n
}

func rating(w mihonv1.ContentWarning) sourcev1.ContentRating {
	switch w {
	case mihonv1.ContentWarning_CONTENT_WARNING_SAFE:
		return sourcev1.ContentRating_CONTENT_RATING_SAFE
	case mihonv1.ContentWarning_CONTENT_WARNING_MIXED:
		return sourcev1.ContentRating_CONTENT_RATING_MIXED
	case mihonv1.ContentWarning_CONTENT_WARNING_NSFW:
		return sourcev1.ContentRating_CONTENT_RATING_NSFW
	default:
		return sourcev1.ContentRating_CONTENT_RATING_UNSPECIFIED
	}
}

// toProto builds the source API's Package for v.
func (p *Plugin) toProto(w *packageWorld, v *view) *sourcev1.Package {
	set := p.settings.Load()
	out := &sourcev1.Package{Id: v.id}
	if n := v.newest(); n != nil {
		out.Name, out.Version, out.ContentRating = n.GetName(), n.GetVersionName(), rating(n.GetContentWarning())
	}
	out.Supported = v.best() != nil
	for _, o := range v.offers {
		out.Offers = append(out.Offers, &sourcev1.PackageOffer{
			OriginId: o.GetStoreId(), OriginName: w.storeNames[o.GetStoreId()], Version: o.GetVersionName(), Supported: o.GetSupported(),
		})
		out.UpdateAvailable = out.UpdateAvailable || o.GetUpdateAvailable()
	}
	addLang := func(lang string) {
		if lang != "" && !slices.Contains(out.Langs, lang) {
			out.Langs = append(out.Langs, lang)
		}
	}
	if e := v.installed; e != nil {
		out.Installed, out.InstalledVersion, out.Error = true, e.GetVersionName(), e.GetLoadError()
		out.Details = details(e)
		if out.Name == "" {
			out.Name, out.Version, out.ContentRating = e.GetName(), e.GetVersionName(), rating(e.GetContentWarning())
		}
		out.Settings = &sourcev1.PackageSettings{EnabledLangs: slices.Clone(e.GetEnabledLangs())}
		for _, s := range e.GetSources() {
			addLang(s.GetLang())
			out.Sources = append(out.Sources, &sourcev1.PackageSource{
				Id: s.GetId(), Name: s.GetName(), Lang: s.GetLang(), Available: s.GetAvailable() && set.offers(s.GetLang()),
			})
		}
		if !p.managed {
			out.IconUrl = "/v1/packages/" + v.id + "/icon"
		}
	} else if b := v.newest(); b != nil {
		out.Settings = &sourcev1.PackageSettings{}
		for _, s := range b.GetSources() {
			addLang(s.GetLang())
			out.Sources = append(out.Sources, &sourcev1.PackageSource{
				Id: s.GetId(), Name: s.GetName(), Lang: s.GetLang(), Available: set.offers(s.GetLang()),
			})
		}
		if !p.managed && b.GetIconUrl() != "" {
			out.IconUrl = "/v1/packages/" + v.id + "/icon"
		}
	}
	return out
}

func matches(pkg *sourcev1.Package, r *sourcev1.ListPackagesRequest) bool {
	if q := strings.ToLower(strings.TrimSpace(r.GetQuery())); q != "" {
		hit := strings.Contains(strings.ToLower(pkg.GetName()), q) || strings.Contains(strings.ToLower(pkg.GetId()), q)
		for _, s := range pkg.GetSources() {
			hit = hit || strings.Contains(strings.ToLower(s.GetName()), q)
		}
		if !hit {
			return false
		}
	}
	switch {
	case r.GetLang() != "" && !slices.Contains(pkg.GetLangs(), r.GetLang()):
		return false
	case r.GetInstalledOnly() && !pkg.GetInstalled():
		return false
	case r.GetUpdatesOnly() && !pkg.GetUpdateAvailable():
		return false
	case r.GetOriginId() != "" && !slices.ContainsFunc(pkg.GetOffers(), func(o *sourcev1.PackageOffer) bool { return o.GetOriginId() == r.GetOriginId() }):
		return false
	}
	return true
}

// op runs one package operation and records it.
func (p *Plugin) op(name string, f func() error) error {
	started := time.Now()
	err := f()
	p.metrics.packageOp(name, err)
	p.metrics.call(name, err, time.Since(started))
	return err
}

func (p *Plugin) ListPackages(ctx context.Context, r *sourcev1.ListPackagesRequest) (*sourcev1.ListPackagesResponse, error) {
	res := &sourcev1.ListPackagesResponse{}
	err := p.op("ListPackages", func() error {
		u, err := p.upstream()
		if err != nil {
			return err
		}
		w, err := p.world(ctx, u)
		if err != nil {
			return err
		}
		for _, v := range w.packages {
			if pkg := p.toProto(w, v); matches(pkg, r) {
				res.Packages = append(res.Packages, pkg)
			}
		}
		sort.Slice(res.Packages, func(i, j int) bool {
			return strings.ToLower(res.Packages[i].GetName()) < strings.ToLower(res.Packages[j].GetName())
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// lookup finds one package, or NOT_FOUND.
func (p *Plugin) lookup(ctx context.Context, id string) (*upstream, *packageWorld, *view, error) {
	if !plugin.PackageIDPattern.MatchString(id) {
		return nil, nil, nil, plugin.ErrNotFound("package %q not found", id)
	}
	u, err := p.upstream()
	if err != nil {
		return nil, nil, nil, err
	}
	w, err := p.world(ctx, u)
	if err != nil {
		return nil, nil, nil, err
	}
	v, ok := w.packages[id]
	if !ok {
		return nil, nil, nil, plugin.ErrNotFound("package %q not found", id)
	}
	return u, w, v, nil
}

// current reads a package again after a change.
func (p *Plugin) current(ctx context.Context, id string) (*sourcev1.Package, error) {
	_, w, v, err := p.lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	return p.toProto(w, v), nil
}

func (p *Plugin) GetPackage(ctx context.Context, r *sourcev1.GetPackageRequest) (*sourcev1.GetPackageResponse, error) {
	var pkg *sourcev1.Package
	err := p.op("GetPackage", func() (err error) {
		pkg, err = p.current(ctx, r.GetPackageId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.GetPackageResponse{Package: pkg}, nil
}

// storeFailure turns mihon-source's store reasons into the source API's.
func storeFailure(err error) error {
	var pe *plugin.Error
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Reason {
	case reasonFetchFailed:
		return plugin.ErrStoreFetchFailed("%s", pe.Message)
	case reasonInvalid:
		return plugin.ErrStoreInvalid("%s", pe.Message)
	}
	return err
}

// installOffer installs one store's offer and logs it.
func (p *Plugin) installOffer(ctx context.Context, u *upstream, offer *mihonv1.AvailableExtension) error {
	_, err := call(ctx, u, func() (*connect.Response[mihonv1.InstallFromStoreResponse], error) {
		return u.stores.InstallFromStore(ctx, connect.NewRequest(&mihonv1.InstallFromStoreRequest{StoreId: offer.GetStoreId(), PkgName: offer.GetPkgName()}))
	})
	if err != nil {
		return storeFailure(err)
	}
	p.log.Info("package installed", "package", offer.GetPkgName(), "version", offer.GetVersionName(), "store", offer.GetStoreId())
	p.cat.invalidate() // its sources changed
	return nil
}

// InstallPackage installs the package from its best offer (or the origin
// asked for). The version already installed is a no-op; mihon-source keeps
// a cancelled install all-or-nothing.
func (p *Plugin) InstallPackage(ctx context.Context, r *sourcev1.InstallPackageRequest) (*sourcev1.InstallPackageResponse, error) {
	var pkg *sourcev1.Package
	err := p.op("InstallPackage", func() error {
		u, _, v, err := p.lookup(ctx, r.GetPackageId())
		if err != nil {
			return err
		}
		offer := v.best()
		if r.GetOriginId() != "" {
			i := slices.IndexFunc(v.offers, func(o *mihonv1.AvailableExtension) bool { return o.GetStoreId() == r.GetOriginId() })
			if i < 0 {
				return plugin.ErrInvalidArgument("origin %q doesn't offer %q", r.GetOriginId(), r.GetPackageId())
			}
			offer = v.offers[i]
		}
		switch {
		case offer == nil:
			return plugin.Errorf(plugin.CodeFailedPrecondition, "no store offers a version of %q that this plugin can run", r.GetPackageId())
		case !offer.GetSupported():
			return plugin.Errorf(plugin.CodeFailedPrecondition, "%q's version %s from %s can't run here", r.GetPackageId(), offer.GetVersionName(), offer.GetStoreId())
		case v.installed == nil || v.installed.GetVersionCode() != offer.GetVersionCode() || v.installed.GetLoadError() != "":
			if err := p.installOffer(ctx, u, offer); err != nil {
				return err
			}
		}
		pkg, err = p.current(ctx, r.GetPackageId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.InstallPackageResponse{Package: pkg}, nil
}

// UpdatePackage installs the newest supported offer when it's newer than the
// installed version; up to date is a no-op.
func (p *Plugin) UpdatePackage(ctx context.Context, r *sourcev1.UpdatePackageRequest) (*sourcev1.UpdatePackageResponse, error) {
	var pkg *sourcev1.Package
	err := p.op("UpdatePackage", func() error {
		u, _, v, err := p.lookup(ctx, r.GetPackageId())
		if err != nil {
			return err
		}
		if v.installed == nil {
			return plugin.Errorf(plugin.CodeFailedPrecondition, "package %q isn't installed", r.GetPackageId())
		}
		var newest *mihonv1.AvailableExtension
		for _, o := range v.offers {
			if o.GetSupported() && (newest == nil || o.GetVersionCode() > newest.GetVersionCode()) {
				newest = o
			}
		}
		if newest != nil && newest.GetVersionCode() > v.installed.GetVersionCode() {
			if err := p.installOffer(ctx, u, newest); err != nil {
				return err
			}
		}
		pkg, err = p.current(ctx, r.GetPackageId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.UpdatePackageResponse{Package: pkg}, nil
}

// UninstallPackage removes the package; one that isn't installed is a no-op.
func (p *Plugin) UninstallPackage(ctx context.Context, r *sourcev1.UninstallPackageRequest) (*sourcev1.UninstallPackageResponse, error) {
	err := p.op("UninstallPackage", func() error {
		u, _, v, err := p.lookup(ctx, r.GetPackageId())
		if err != nil || v.installed == nil {
			return err
		}
		_, err = call(ctx, u, func() (*connect.Response[mihonv1.UninstallExtensionResponse], error) {
			return u.extensions.UninstallExtension(ctx, connect.NewRequest(&mihonv1.UninstallExtensionRequest{PkgName: r.GetPackageId()}))
		})
		if err == nil {
			p.log.Info("package uninstalled", "package", r.GetPackageId())
			p.cat.invalidate()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.UninstallPackageResponse{}, nil
}

// SetPackageSettings sets an installed package's languages in mihon-source.
func (p *Plugin) SetPackageSettings(ctx context.Context, r *sourcev1.SetPackageSettingsRequest) (*sourcev1.SetPackageSettingsResponse, error) {
	var pkg *sourcev1.Package
	err := p.op("SetPackageSettings", func() error {
		u, _, v, err := p.lookup(ctx, r.GetPackageId())
		if err != nil {
			return err
		}
		if v.installed == nil {
			return plugin.Errorf(plugin.CodeFailedPrecondition, "install %q before setting its languages", r.GetPackageId())
		}
		_, err = call(ctx, u, func() (*connect.Response[mihonv1.SetExtensionLanguagesResponse], error) {
			return u.extensions.SetExtensionLanguages(ctx, connect.NewRequest(&mihonv1.SetExtensionLanguagesRequest{
				PkgName: r.GetPackageId(), EnabledLangs: r.GetSettings().GetEnabledLangs(),
			}))
		})
		if err != nil {
			return err
		}
		p.log.Info("package languages set", "package", r.GetPackageId(), "langs", r.GetSettings().GetEnabledLangs())
		p.cat.invalidate()
		pkg, err = p.current(ctx, r.GetPackageId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.SetPackageSettingsResponse{Package: pkg}, nil
}
