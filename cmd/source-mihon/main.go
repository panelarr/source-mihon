// SPDX-License-Identifier: GPL-3.0-or-later

// Command source-mihon is a Panelarr source plugin that serves the sources
// of a mihon-source instance.
//
// Run it standalone with --listen (default :8904), or let the Panelarr
// server start it as a managed plugin. The host sends the configuration with
// SetConfig; standalone, it can also come from --config or the environment:
//
//	MIHON_SOURCE_URL       mihon-source's address (default http://mihon-source:8902)
//	MIHON_SOURCE_API_KEY   a mihon-source client key (or MIHON_SOURCE_API_KEY_FILE)
//
// "healthcheck" probes a running instance, for container health checks, and
// "version" prints the version.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/internal/buildinfo"
	"github.com/panelarr/source-mihon/internal/mihon"
)

func main() {
	if len(os.Args) > 1 && !plugin.IsManaged() {
		switch os.Args[1] {
		case "version":
			fmt.Println(buildinfo.Version)
			return
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:], os.Stderr))
		}
	}
	// The plugin's own key may come from a file too (Docker secrets).
	if os.Getenv("PLUGIN_API_KEY") == "" {
		if k := env("PLUGIN_API_KEY"); k != "" {
			_ = os.Setenv("PLUGIN_API_KEY", k)
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	p, err := mihon.New(context.Background(), mihon.Options{Log: log, Managed: plugin.IsManaged(), Initial: fromEnv()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "source-mihon:", err)
		os.Exit(2)
	}
	defer p.Close()
	plugin.Main(p, plugin.WithDefaultListen(":8904"), plugin.WithRoutes(
		plugin.Route{Pattern: "GET /metrics", Handler: p.Metrics().Handler()},
		plugin.Route{Pattern: "GET /v1/sources/{sourceId}/icon", Handler: p.IconHandler()},
	))
}

// fromEnv reads the standalone configuration from the environment. A value
// can also be given as a file: NAME_FILE holds its path (Docker secrets).
func fromEnv() map[string]any {
	values := map[string]any{}
	if v := env("MIHON_SOURCE_URL"); v != "" {
		values["mihon_source_url"] = v
	}
	if v := env("MIHON_SOURCE_API_KEY"); v != "" {
		values["api_key"] = v
	}
	return values
}

func env(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the operator chose the file
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("PLUGIN_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
