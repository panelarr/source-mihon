// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"
)

// The package calls of the source API. source-mihon doesn't declare the
// PACKAGES capability yet, so they answer UNIMPLEMENTED.

func (p *Plugin) ListPackages(context.Context, *sourcev1.ListPackagesRequest) (*sourcev1.ListPackagesResponse, error) {
	return nil, plugin.ErrUnimplemented("ListPackages")
}

func (p *Plugin) GetPackage(context.Context, *sourcev1.GetPackageRequest) (*sourcev1.GetPackageResponse, error) {
	return nil, plugin.ErrUnimplemented("GetPackage")
}

func (p *Plugin) InstallPackage(context.Context, *sourcev1.InstallPackageRequest) (*sourcev1.InstallPackageResponse, error) {
	return nil, plugin.ErrUnimplemented("InstallPackage")
}

func (p *Plugin) UpdatePackage(context.Context, *sourcev1.UpdatePackageRequest) (*sourcev1.UpdatePackageResponse, error) {
	return nil, plugin.ErrUnimplemented("UpdatePackage")
}

func (p *Plugin) UninstallPackage(context.Context, *sourcev1.UninstallPackageRequest) (*sourcev1.UninstallPackageResponse, error) {
	return nil, plugin.ErrUnimplemented("UninstallPackage")
}

func (p *Plugin) SetPackageSettings(context.Context, *sourcev1.SetPackageSettingsRequest) (*sourcev1.SetPackageSettingsResponse, error) {
	return nil, plugin.ErrUnimplemented("SetPackageSettings")
}
