// SPDX-License-Identifier: GPL-3.0-or-later

// Package buildinfo holds the version, set at build time.
package buildinfo

// Version is set with -ldflags "-X github.com/panelarr/source-mihon/internal/buildinfo.Version=…".
var Version = "dev"
