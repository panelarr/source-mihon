// SPDX-License-Identifier: GPL-3.0-or-later

// Package mihon is source-mihon: a Panelarr source plugin that serves the
// sources of a mihon-source instance. Every source API call goes through to
// mihon-source over gRPC (with retries while it can't be reached), network
// profiles the host pushes are forwarded, and the extension stores and
// extensions named in the plugin's configuration are kept in mihon-source.
package mihon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/internal/buildinfo"
)

// ID is the plugin's stable identifier.
const ID = "source-mihon"

// Plugin is source-mihon.
type Plugin struct {
	*plugin.PluginConfig

	log      *slog.Logger
	metrics  *Metrics
	managed  bool
	up       atomic.Pointer[upstream]
	settings atomic.Pointer[Settings]
	stores   *storeSync
	cat      catalogue
}

// Options configure [New].
type Options struct {
	Log *slog.Logger
	// Managed says the host started the plugin as a child process, so it
	// can't fetch paths relative to the plugin's address.
	Managed bool
	// Initial is applied as the configuration before serving, e.g. from the
	// environment. The host's SetConfig replaces it.
	Initial map[string]any
	// SyncEvery is how often the stores and extensions are reconciled with
	// mihon-source. Default 15 minutes.
	SyncEvery time.Duration
}

// New returns the plugin, configured with the defaults (and opts.Initial).
func New(ctx context.Context, opts Options) (*Plugin, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.SyncEvery == 0 {
		opts.SyncEvery = 15 * time.Minute
	}
	p := &Plugin{log: opts.Log, metrics: NewMetrics(), managed: opts.Managed}
	p.stores = newStoreSync(p, opts.SyncEvery)
	p.PluginConfig = plugin.NewPluginConfig(schema(), p.apply)
	if err := p.Set(ctx, opts.Initial); err != nil {
		return nil, err
	}
	return p, nil
}

// Close stops background work and connections.
func (p *Plugin) Close() {
	p.stores.stop()
	if u := p.up.Swap(nil); u != nil {
		u.close()
	}
}

// Metrics returns the plugin's metrics.
func (p *Plugin) Metrics() *Metrics { return p.metrics }

// apply switches to a new configuration: a new connection to mihon-source,
// then the stores and extensions are reconciled in the background.
func (p *Plugin) apply(ctx context.Context, values map[string]any) error {
	s, err := parseSettings(values)
	if err != nil {
		return plugin.ErrInvalidArgument("%v", err)
	}
	next := newUpstream(s)
	// A key mihon-source rejects is a configuration error the admin should
	// see now. mihon-source being down is not: it may still be starting.
	if s.APIKey != "" {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := next.source.ListSources(checkCtx, &sourcev1.ListSourcesRequest{Lang: "-"})
		cancel()
		if code := plugin.ErrorCode(err); code == plugin.CodeUnauthenticated || code == plugin.CodePermissionDenied {
			next.close()
			return plugin.ErrInvalidArgument("mihon-source at %s rejected the API key", s.URL)
		}
	}
	if old := p.up.Swap(next); old != nil {
		old.close()
	}
	p.settings.Store(&s)
	p.log.Info("configured", "mihon_source_url", s.URL, "api_key", s.APIKey != "", "stores", len(s.Stores), "extensions", len(s.Extensions))
	p.stores.kick()
	return nil
}

// upstream returns the connection, or an error when the plugin isn't
// configured yet.
func (p *Plugin) upstream() (*upstream, error) {
	u := p.up.Load()
	if u == nil {
		return nil, plugin.Errorf(plugin.CodeFailedPrecondition, "source-mihon isn't configured: set mihon_source_url")
	}
	return u, nil
}

// pass forwards one call to mihon-source and records it.
func pass[Req, Res any](ctx context.Context, p *Plugin, method string, req *Req, f func(client *upstream, ctx context.Context, req *Req) (*Res, error)) (*Res, error) {
	started := time.Now()
	u, err := p.upstream()
	var res *Res
	if err == nil {
		res, err = call(ctx, u, func() (*Res, error) { return f(u, ctx, req) })
	}
	p.metrics.call(method, err, time.Since(started))
	return res, err
}

func (p *Plugin) GetInfo(context.Context, *sourcev1.GetInfoRequest) (*sourcev1.GetInfoResponse, error) {
	return &sourcev1.GetInfoResponse{Info: &sourcev1.PluginInfo{
		Id:        ID,
		Name:      "Mihon extensions",
		Version:   buildinfo.Version,
		Contracts: []string{plugin.Protocol},
		Capabilities: []sourcev1.PluginCapability{
			sourcev1.PluginCapability_PLUGIN_CAPABILITY_NETWORK_PROFILES,
			sourcev1.PluginCapability_PLUGIN_CAPABILITY_DYNAMIC_SOURCES,
			sourcev1.PluginCapability_PLUGIN_CAPABILITY_PACKAGES,
		},
		// mihon-source also applies each extension's own rate limits and
		// the profile's. These are what a host should keep to per source.
		Limits: &sourcev1.Limits{
			MaxConcurrencyPerSource: 2,
			DefaultRateLimit: &sourcev1.RateLimit{
				Requests: 30, Period: durationpb.New(time.Minute), Burst: 5,
				Scope: sourcev1.RateLimitScope_RATE_LIMIT_SCOPE_SOURCE,
			},
		},
		ConfigSchema: p.SchemaStruct(),
		CommonServices: []sourcev1.CommonService{
			sourcev1.CommonService_COMMON_SERVICE_NETWORK_PROFILE,
			sourcev1.CommonService_COMMON_SERVICE_SOLVER,
		},
	}}, nil
}

// Health is serving when mihon-source is; degraded when mihon-source is, or
// when keeping its stores and extensions failed.
func (p *Plugin) Health(ctx context.Context, _ *sourcev1.HealthRequest) (*sourcev1.HealthResponse, error) {
	u, err := p.upstream()
	if err != nil {
		return &sourcev1.HealthResponse{Status: sourcev1.HealthStatus_HEALTH_STATUS_NOT_SERVING, Message: err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := u.source.Health(ctx)
	if err != nil {
		p.metrics.upstreamUp(false)
		return &sourcev1.HealthResponse{
			Status:  sourcev1.HealthStatus_HEALTH_STATUS_NOT_SERVING,
			Message: "mihon-source at " + u.url + " can't be reached",
		}, nil
	}
	p.metrics.upstreamUp(true)
	status, messages := h.GetStatus(), []string{}
	if h.GetMessage() != "" {
		messages = append(messages, "mihon-source: "+h.GetMessage())
	}
	if problem := p.stores.problem(); problem != "" {
		messages = append(messages, problem)
		if status == sourcev1.HealthStatus_HEALTH_STATUS_SERVING {
			status = sourcev1.HealthStatus_HEALTH_STATUS_DEGRADED
		}
	}
	return &sourcev1.HealthResponse{Status: status, Message: strings.Join(messages, "; ")}, nil
}

func (p *Plugin) ListSources(ctx context.Context, r *sourcev1.ListSourcesRequest) (*sourcev1.ListSourcesResponse, error) {
	started := time.Now()
	u, err := p.upstream()
	var all []*sourcev1.Source
	if err == nil {
		all, err = p.refreshCatalogue(ctx, u, r.GetLang())
	}
	p.metrics.call("ListSources", err, time.Since(started))
	if err != nil {
		return nil, err
	}
	set := p.settings.Load()
	res := &sourcev1.ListSourcesResponse{}
	for _, s := range all {
		if set.offers(s.GetLang()) {
			p.fixIcon(s)
			res.Sources = append(res.Sources, s)
		}
	}
	p.metrics.sources(len(res.GetSources()), r.GetLang())
	return res, nil
}

func (p *Plugin) GetSource(ctx context.Context, r *sourcev1.GetSourceRequest) (*sourcev1.GetSourceResponse, error) {
	res, err := pass(ctx, p, "GetSource", r, func(u *upstream, ctx context.Context, r *sourcev1.GetSourceRequest) (*sourcev1.GetSourceResponse, error) {
		return u.source.GetSource(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	src := res.GetSource()
	if !p.settings.Load().offers(src.GetLang()) {
		return nil, plugin.ErrNotFound("source %s isn't offered: its language %q isn't among the plugin's languages", src.GetId(), src.GetLang())
	}
	if _, pkg, ok := p.cat.lookup(src.GetId()); ok {
		src.PackageId = pkg
	} else if u, err := p.upstream(); err == nil {
		src.PackageId = p.sourcePackages(ctx, u)[src.GetId()]
	}
	p.fixIcon(src)
	return res, nil
}

// fixIcon keeps icon paths that are relative to mihon-source working: a
// standalone source-mihon serves the same paths (see IconHandler); a
// managed one has no address of its own, so the icon is left out.
func (p *Plugin) fixIcon(s *sourcev1.Source) {
	if s != nil && p.managed && strings.HasPrefix(s.GetIconUrl(), "/") {
		s.IconUrl = ""
	}
}

func (p *Plugin) GetFilters(ctx context.Context, r *sourcev1.GetFiltersRequest) (*sourcev1.GetFiltersResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "GetFilters", r, func(u *upstream, ctx context.Context, r *sourcev1.GetFiltersRequest) (*sourcev1.GetFiltersResponse, error) {
		return u.source.GetFilters(ctx, r)
	})
}

func (p *Plugin) Popular(ctx context.Context, r *sourcev1.PopularRequest) (*sourcev1.PopularResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "Popular", r, func(u *upstream, ctx context.Context, r *sourcev1.PopularRequest) (*sourcev1.PopularResponse, error) {
		return u.source.Popular(ctx, r)
	})
}

func (p *Plugin) Latest(ctx context.Context, r *sourcev1.LatestRequest) (*sourcev1.LatestResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "Latest", r, func(u *upstream, ctx context.Context, r *sourcev1.LatestRequest) (*sourcev1.LatestResponse, error) {
		return u.source.Latest(ctx, r)
	})
}

func (p *Plugin) Search(ctx context.Context, r *sourcev1.SearchRequest) (*sourcev1.SearchResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "Search", r, func(u *upstream, ctx context.Context, r *sourcev1.SearchRequest) (*sourcev1.SearchResponse, error) {
		return u.source.Search(ctx, r)
	})
}

func (p *Plugin) ResolveUrl(ctx context.Context, r *sourcev1.ResolveUrlRequest) (*sourcev1.ResolveUrlResponse, error) { //nolint:revive // named after the RPC
	if r.GetSourceId() != "" {
		if err := p.offered(ctx, r.GetSourceId()); err != nil {
			return nil, err
		}
	}
	res, err := pass(ctx, p, "ResolveUrl", r, func(u *upstream, ctx context.Context, r *sourcev1.ResolveUrlRequest) (*sourcev1.ResolveUrlResponse, error) {
		return u.source.ResolveUrl(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	if err := p.offered(ctx, res.GetSourceId()); err != nil {
		return nil, plugin.ErrNotFound("no offered source knows %q", r.GetUrl())
	}
	return res, nil
}

func (p *Plugin) GetSeries(ctx context.Context, r *sourcev1.GetSeriesRequest) (*sourcev1.GetSeriesResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "GetSeries", r, func(u *upstream, ctx context.Context, r *sourcev1.GetSeriesRequest) (*sourcev1.GetSeriesResponse, error) {
		return u.source.GetSeries(ctx, r)
	})
}

func (p *Plugin) ListChapters(ctx context.Context, r *sourcev1.ListChaptersRequest) (*sourcev1.ListChaptersResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "ListChapters", r, func(u *upstream, ctx context.Context, r *sourcev1.ListChaptersRequest) (*sourcev1.ListChaptersResponse, error) {
		return u.source.ListChapters(ctx, r)
	})
}

func (p *Plugin) ListPages(ctx context.Context, r *sourcev1.ListPagesRequest) (*sourcev1.ListPagesResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "ListPages", r, func(u *upstream, ctx context.Context, r *sourcev1.ListPagesRequest) (*sourcev1.ListPagesResponse, error) {
		return u.source.ListPages(ctx, r)
	})
}

func (p *Plugin) GetSettingsSchema(ctx context.Context, r *sourcev1.GetSettingsSchemaRequest) (*sourcev1.GetSettingsSchemaResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "GetSettingsSchema", r, func(u *upstream, ctx context.Context, r *sourcev1.GetSettingsSchemaRequest) (*sourcev1.GetSettingsSchemaResponse, error) {
		return u.source.GetSettingsSchema(ctx, r)
	})
}

func (p *Plugin) GetSettings(ctx context.Context, r *sourcev1.GetSettingsRequest) (*sourcev1.GetSettingsResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "GetSettings", r, func(u *upstream, ctx context.Context, r *sourcev1.GetSettingsRequest) (*sourcev1.GetSettingsResponse, error) {
		return u.source.GetSettings(ctx, r)
	})
}

func (p *Plugin) SetSettings(ctx context.Context, r *sourcev1.SetSettingsRequest) (*sourcev1.SetSettingsResponse, error) {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return nil, err
	}
	return pass(ctx, p, "SetSettings", r, func(u *upstream, ctx context.Context, r *sourcev1.SetSettingsRequest) (*sourcev1.SetSettingsResponse, error) {
		return u.source.SetSettings(ctx, r)
	})
}

// SetNetworkProfile forwards the host's profile for a source to
// mihon-source, which applies it to the source's requests.
func (p *Plugin) SetNetworkProfile(ctx context.Context, r *sourcev1.SetNetworkProfileRequest) (*sourcev1.SetNetworkProfileResponse, error) {
	res, err := pass(ctx, p, "SetNetworkProfile", r, func(u *upstream, ctx context.Context, r *sourcev1.SetNetworkProfileRequest) (*sourcev1.SetNetworkProfileResponse, error) {
		return &sourcev1.SetNetworkProfileResponse{}, u.source.SetNetworkProfile(ctx, r.GetSourceId(), r.GetProfile())
	})
	if err != nil {
		return nil, err
	}
	p.log.Info("network profile forwarded", "source", r.GetSourceId(), "profile", r.GetProfile().GetName(), "removed", r.GetProfile() == nil)
	return res, nil
}

// FetchImage streams the image from mihon-source. The stream is retried
// only while nothing has been sent yet.
func (p *Plugin) FetchImage(ctx context.Context, r *sourcev1.FetchImageRequest, send plugin.ImageSendFunc) error {
	if err := p.offered(ctx, r.GetSourceId()); err != nil {
		return err
	}
	started := time.Now()
	err := p.fetchImage(ctx, r, send)
	p.metrics.call("FetchImage", err, time.Since(started))
	return err
}

func (p *Plugin) fetchImage(ctx context.Context, r *sourcev1.FetchImageRequest, send plugin.ImageSendFunc) error {
	u, err := p.upstream()
	if err != nil {
		return err
	}
	stream, err := call(ctx, u, func() (*imageStream, error) {
		s, err := u.source.FetchImage(ctx, r)
		if err != nil {
			return nil, err
		}
		// The first message tells whether the image exists.
		first, err := s.Recv()
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		return &imageStream{first: first, rest: s}, nil
	})
	if err != nil {
		return err
	}
	defer stream.rest.Close()
	if err := send(stream.first); err != nil {
		return err
	}
	for {
		msg, err := stream.rest.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return u.mapError(err)
		}
		if err := send(msg); err != nil {
			return err
		}
	}
}

type imageStream struct {
	first *sourcev1.FetchImageResponse
	rest  interface {
		Recv() (*sourcev1.FetchImageResponse, error)
		Close() error
	}
}

// Configured returns the settings in effect (secrets included), for tests
// and diagnostics.
func (p *Plugin) Configured() Settings {
	if s := p.settings.Load(); s != nil {
		c := *s
		c.Stores = append([]StoreSetting(nil), s.Stores...)
		c.Extensions = append([]string(nil), s.Extensions...)
		return c
	}
	return Settings{}
}

var _ plugin.Plugin = (*Plugin)(nil)
