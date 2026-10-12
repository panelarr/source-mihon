// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"

	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"
)

// The job calls aren't served yet: the plugin doesn't declare JOBS.

func (p *Plugin) ListJobs(context.Context, *sourcev1.ListJobsRequest) (*sourcev1.ListJobsResponse, error) {
	return nil, plugin.ErrUnimplemented("ListJobs")
}

func (p *Plugin) GetJob(context.Context, *sourcev1.GetJobRequest) (*sourcev1.GetJobResponse, error) {
	return nil, plugin.ErrUnimplemented("GetJob")
}

func (p *Plugin) ListJobRuns(context.Context, *sourcev1.ListJobRunsRequest) (*sourcev1.ListJobRunsResponse, error) {
	return nil, plugin.ErrUnimplemented("ListJobRuns")
}

func (p *Plugin) GetJobRun(context.Context, *sourcev1.GetJobRunRequest) (*sourcev1.GetJobRunResponse, error) {
	return nil, plugin.ErrUnimplemented("GetJobRun")
}

func (p *Plugin) RunJob(context.Context, *sourcev1.RunJobRequest) (*sourcev1.RunJobResponse, error) {
	return nil, plugin.ErrUnimplemented("RunJob")
}

func (p *Plugin) CancelJobRun(context.Context, *sourcev1.CancelJobRunRequest) (*sourcev1.CancelJobRunResponse, error) {
	return nil, plugin.ErrUnimplemented("CancelJobRun")
}
