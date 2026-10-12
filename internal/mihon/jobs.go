// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// The job calls: the plugin's own jobs (IDs starting "plugin.", run IDs
// "plugin-") run in the plugin; everything else is mihon-source's jobs,
// passed through. mihon-source's job messages and the source API's are the
// same on the wire, so they're converted by re-encoding.

const (
	ownJobPrefix = "plugin."
	ownRunPrefix = "plugin-"
)

func ownJob(id string) bool { return strings.HasPrefix(id, ownJobPrefix) }

func ownRun(id string) bool { return strings.HasPrefix(id, ownRunPrefix) }

// convert re-encodes from as to, which has the same fields.
func convert[T proto.Message](from proto.Message, to T) (T, error) {
	b, err := proto.Marshal(from)
	if err == nil {
		err = proto.Unmarshal(b, to)
	}
	if err != nil {
		return to, plugin.Errorf(plugin.CodeInternal, "converting %T: %v", from, err)
	}
	return to, nil
}

// upstreamJobs calls one of mihon-source's job calls: req is re-encoded as
// the mihon-source request, and the answer as the source API's.
func upstreamJobs[UReq, URes any, Res proto.Message](
	ctx context.Context, p *Plugin, method string, req proto.Message, ureq *UReq, res Res,
	f func(c *upstream, ctx context.Context, r *connect.Request[UReq]) (*connect.Response[URes], error),
) (Res, error) {
	var zero Res
	err := p.op(method, func() error {
		u, err := p.upstream()
		if err != nil {
			return err
		}
		in, err := convert(req, any(ureq).(proto.Message))
		if err != nil {
			return err
		}
		out, err := call(ctx, u, func() (*connect.Response[URes], error) {
			return f(u, ctx, connect.NewRequest(any(in).(*UReq)))
		})
		if err != nil {
			return u.mapError(err)
		}
		_, err = convert(any(out.Msg).(proto.Message), res)
		return err
	})
	if err != nil {
		return zero, err
	}
	return res, nil
}

// ListJobs lists the plugin's jobs, then mihon-source's. When mihon-source
// can't be reached, only the plugin's are listed.
func (p *Plugin) ListJobs(ctx context.Context, r *sourcev1.ListJobsRequest) (*sourcev1.ListJobsResponse, error) {
	own, err := p.jobs.ListJobs(ctx, r)
	if err != nil {
		return nil, err
	}
	theirs, err := upstreamJobs(ctx, p, "ListJobs", r, &mihonv1.ListJobsRequest{}, &sourcev1.ListJobsResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.ListJobsRequest]) (*connect.Response[mihonv1.ListJobsResponse], error) {
			return c.jobs.ListJobs(ctx, r)
		})
	switch {
	case err == nil:
		own.Jobs = append(own.Jobs, theirs.GetJobs()...)
	case p.up.Load() == nil, plugin.ErrorReason(err) == plugin.ReasonSourceDown, plugin.ErrorCode(err) == plugin.CodeUnimplemented:
		// Not configured, mihon-source is down or has no jobs: the plugin's own still help.
		p.log.Warn("listing mihon-source's jobs", "error", err)
	default:
		return nil, err
	}
	return own, nil
}

func (p *Plugin) GetJob(ctx context.Context, r *sourcev1.GetJobRequest) (*sourcev1.GetJobResponse, error) {
	if ownJob(r.GetJobId()) {
		return p.jobs.GetJob(ctx, r)
	}
	return upstreamJobs(ctx, p, "GetJob", r, &mihonv1.GetJobRequest{}, &sourcev1.GetJobResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.GetJobRequest]) (*connect.Response[mihonv1.GetJobResponse], error) {
			return c.jobs.GetJob(ctx, r)
		})
}

func (p *Plugin) ListJobRuns(ctx context.Context, r *sourcev1.ListJobRunsRequest) (*sourcev1.ListJobRunsResponse, error) {
	if ownJob(r.GetJobId()) {
		return p.jobs.ListJobRuns(ctx, r)
	}
	return upstreamJobs(ctx, p, "ListJobRuns", r, &mihonv1.ListJobRunsRequest{}, &sourcev1.ListJobRunsResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.ListJobRunsRequest]) (*connect.Response[mihonv1.ListJobRunsResponse], error) {
			return c.jobs.ListJobRuns(ctx, r)
		})
}

func (p *Plugin) GetJobRun(ctx context.Context, r *sourcev1.GetJobRunRequest) (*sourcev1.GetJobRunResponse, error) {
	if ownRun(r.GetRunId()) {
		return p.jobs.GetJobRun(ctx, r)
	}
	return upstreamJobs(ctx, p, "GetJobRun", r, &mihonv1.GetJobRunRequest{}, &sourcev1.GetJobRunResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.GetJobRunRequest]) (*connect.Response[mihonv1.GetJobRunResponse], error) {
			return c.jobs.GetJobRun(ctx, r)
		})
}

// RunJob starts a run. mihon-source records its runs as started by the
// plugin's API client; on_behalf_of only reaches the plugin's own runs.
func (p *Plugin) RunJob(ctx context.Context, r *sourcev1.RunJobRequest) (*sourcev1.RunJobResponse, error) {
	if ownJob(r.GetJobId()) {
		return p.jobs.RunJob(ctx, r)
	}
	res, err := upstreamJobs(ctx, p, "RunJob", r, &mihonv1.RunJobRequest{}, &sourcev1.RunJobResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.RunJobRequest]) (*connect.Response[mihonv1.RunJobResponse], error) {
			return c.jobs.RunJob(ctx, r)
		})
	if err == nil {
		p.log.Info("mihon-source job started", "job", r.GetJobId(), "run", res.GetRun().GetId(), "on_behalf_of", r.GetOnBehalfOf())
	}
	return res, err
}

func (p *Plugin) CancelJobRun(ctx context.Context, r *sourcev1.CancelJobRunRequest) (*sourcev1.CancelJobRunResponse, error) {
	if ownRun(r.GetRunId()) {
		return p.jobs.CancelJobRun(ctx, r)
	}
	return upstreamJobs(ctx, p, "CancelJobRun", r, &mihonv1.CancelJobRunRequest{}, &sourcev1.CancelJobRunResponse{},
		func(c *upstream, ctx context.Context, r *connect.Request[mihonv1.CancelJobRunRequest]) (*connect.Response[mihonv1.CancelJobRunResponse], error) {
			return c.jobs.CancelJobRun(ctx, r)
		})
}

// SetSourceHealthCheck turns mihon-source's scheduled checks of a source on
// or off, and returns the source.
func (p *Plugin) SetSourceHealthCheck(ctx context.Context, r *sourcev1.SetSourceHealthCheckRequest) (*sourcev1.SetSourceHealthCheckResponse, error) {
	// A source outside the plugin's languages doesn't exist here.
	if _, err := p.GetSource(ctx, &sourcev1.GetSourceRequest{SourceId: r.GetSourceId()}); err != nil {
		return nil, err
	}
	err := p.op("SetSourceHealthCheck", func() error {
		u, err := p.upstream()
		if err != nil {
			return err
		}
		_, err = call(ctx, u, func() (*connect.Response[mihonv1.SetHealthCheckResponse], error) {
			return u.extensions.SetHealthCheck(ctx, connect.NewRequest(&mihonv1.SetHealthCheckRequest{SourceId: r.GetSourceId(), Enabled: r.GetEnabled()}))
		})
		return u.mapError(err)
	})
	if err != nil {
		return nil, err
	}
	p.log.Info("source health checks set", "source", r.GetSourceId(), "enabled", r.GetEnabled())
	p.cat.invalidate() // the source's health changed
	src, err := p.GetSource(ctx, &sourcev1.GetSourceRequest{SourceId: r.GetSourceId()})
	if err != nil {
		return nil, err
	}
	return &sourcev1.SetSourceHealthCheckResponse{Source: src.GetSource()}, nil
}
