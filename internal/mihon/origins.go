// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"

	"connectrpc.com/connect"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// Origins are mihon-source's extension stores; reloading a package reloads
// the extension in mihon-source.

func toOrigin(st *mihonv1.Store) *sourcev1.Origin {
	name := st.GetName()
	if name == "" {
		name = st.GetUrl()
	}
	return &sourcev1.Origin{
		Id: st.GetId(), Name: name, Url: st.GetUrl(), RefreshedAt: st.GetRefreshedAt(), Error: st.GetLastError(),
		PackageCount: st.GetExtensionCount(), SigningKeyFingerprint: st.GetSigningKeyFingerprint(),
	}
}

func (p *Plugin) ListOrigins(ctx context.Context, _ *sourcev1.ListOriginsRequest) (*sourcev1.ListOriginsResponse, error) {
	res := &sourcev1.ListOriginsResponse{}
	err := p.op("ListOrigins", func() error {
		u, err := p.upstream()
		if err != nil {
			return err
		}
		stores, err := call(ctx, u, func() (*connect.Response[mihonv1.ListStoresResponse], error) {
			return u.stores.ListStores(ctx, connect.NewRequest(&mihonv1.ListStoresRequest{}))
		})
		if err != nil {
			return err
		}
		for _, st := range stores.Msg.GetStores() {
			if plugin.PackageIDPattern.MatchString(st.GetId()) {
				res.Origins = append(res.Origins, toOrigin(st))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// RefreshOrigin refreshes a store in mihon-source. A store that can't be
// fetched or used fails with STORE_FETCH_FAILED or STORE_INVALID.
func (p *Plugin) RefreshOrigin(ctx context.Context, r *sourcev1.RefreshOriginRequest) (*sourcev1.RefreshOriginResponse, error) {
	var origin *sourcev1.Origin
	err := p.op("RefreshOrigin", func() error {
		if !plugin.PackageIDPattern.MatchString(r.GetOriginId()) {
			return plugin.ErrNotFound("origin %q not found", r.GetOriginId())
		}
		u, err := p.upstream()
		if err != nil {
			return err
		}
		res, err := call(ctx, u, func() (*connect.Response[mihonv1.RefreshStoreResponse], error) {
			return u.stores.RefreshStore(ctx, connect.NewRequest(&mihonv1.RefreshStoreRequest{StoreId: r.GetOriginId()}))
		})
		if err != nil {
			return storeFailure(err)
		}
		p.log.Info("origin refreshed", "origin", r.GetOriginId())
		origin = toOrigin(res.Msg.GetStore())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.RefreshOriginResponse{Origin: origin}, nil
}

// ReloadPackage reloads an installed extension from its file in
// mihon-source.
func (p *Plugin) ReloadPackage(ctx context.Context, r *sourcev1.ReloadPackageRequest) (*sourcev1.ReloadPackageResponse, error) {
	var pkg *sourcev1.Package
	err := p.op("ReloadPackage", func() error {
		u, _, v, err := p.lookup(ctx, r.GetPackageId())
		if err != nil {
			return err
		}
		if v.installed == nil {
			return plugin.Errorf(plugin.CodeFailedPrecondition, "package %q isn't installed", r.GetPackageId())
		}
		_, err = call(ctx, u, func() (*connect.Response[mihonv1.ReloadExtensionResponse], error) {
			return u.extensions.ReloadExtension(ctx, connect.NewRequest(&mihonv1.ReloadExtensionRequest{PkgName: r.GetPackageId()}))
		})
		if err != nil {
			return err
		}
		p.log.Info("package reloaded", "package", r.GetPackageId())
		p.cat.invalidate() // its sources may have changed
		pkg, err = p.current(ctx, r.GetPackageId())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &sourcev1.ReloadPackageResponse{Package: pkg}, nil
}

// details describe an installed extension.
func details(e *mihonv1.Extension) *sourcev1.PackageDetails {
	return &sourcev1.PackageDetails{
		SigningFingerprint: e.GetSigningFingerprint(),
		ApiVersion:         e.GetLibVersion(),
		InstalledAt:        e.GetInstalledAt(),
		Sha256:             e.GetSha256(),
		OriginId:           e.GetOrigin().GetStoreId(),
	}
}
