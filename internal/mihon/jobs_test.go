// SPDX-License-Identifier: GPL-3.0-or-later

package mihon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	jobsv1 "github.com/panelarr/source-api/gen/panelarr/jobs/v1"
	sourcev1 "github.com/panelarr/source-api/gen/panelarr/source/v1"
	"github.com/panelarr/source-api/sdk/plugin"
	"github.com/panelarr/source-api/sdk/plugintest"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
	"github.com/panelarr/source-mihon/internal/mihon"
)

func jobIDs(res *sourcev1.ListJobsResponse) string {
	var ids []string
	for _, j := range res.GetJobs() {
		ids = append(ids, j.GetId())
	}
	return strings.Join(ids, ",")
}

func TestJobsOfThePluginAndOfMihonSource(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	// Something for plugin.sync to keep, so it can run.
	p := newPlugin(t, up, map[string]any{"health_checks": map[string]any{"interval": "1d"}}, false)
	ctx := context.Background()

	list, err := p.ListJobs(ctx, &sourcev1.ListJobsRequest{})
	if err != nil || jobIDs(list) != "plugin.sync,store-refresh,source-health" {
		t.Fatalf("jobs: %v, %v", jobIDs(list), err)
	}
	if params := list.GetJobs()[2].GetParams(); len(params) != 2 || params[1].GetDefault() != "popular" {
		t.Fatalf("source-health params: %v", params)
	}

	// mihon-source's jobs pass through, with its errors.
	run, err := p.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "source-health", Params: map[string]string{"source_ids": "1001", "steps": "image"}, OnBehalfOf: "alice"})
	if err != nil || run.GetRun().GetId() != "r1" {
		t.Fatalf("run: %v, %v", run, err)
	}
	_, err = p.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "source-health"})
	var pe *plugin.Error
	if !errors.As(err, &pe) || pe.Code != plugin.CodeAlreadyExists || pe.Reason != plugin.ReasonAlreadyRunning || pe.CurrentRunID != "r1" {
		t.Fatalf("second run: %#v", err)
	}
	got, err := p.GetJobRun(ctx, &sourcev1.GetJobRunRequest{RunId: "r1"})
	if err != nil || got.GetRun().GetParams()["steps"] != "image" {
		t.Fatalf("run r1: %v, %v", got, err)
	}
	job, err := p.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "source-health"})
	if err != nil || !job.GetJob().GetRunning() || job.GetJob().GetCurrentRunId() != "r1" {
		t.Fatalf("job: %v, %v", job, err)
	}
	if c, err := p.CancelJobRun(ctx, &sourcev1.CancelJobRunRequest{RunId: "r1"}); err != nil || c.GetRun().GetStatus() != jobsv1.JobRunStatus_JOB_RUN_STATUS_CANCELLED {
		t.Fatalf("cancel: %v, %v", c, err)
	}
	runs, err := p.ListJobRuns(ctx, &sourcev1.ListJobRunsRequest{JobId: "source-health"})
	if err != nil || len(runs.GetRuns()) != 1 {
		t.Fatalf("runs: %v, %v", runs, err)
	}
	for _, call := range []func() error{
		func() error { _, err := p.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "nope"}); return err },
		func() error { _, err := p.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "nope"}); return err },
		func() error { _, err := p.GetJobRun(ctx, &sourcev1.GetJobRunRequest{RunId: "nope"}); return err },
		func() error { _, err := p.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "plugin.nope"}); return err },
		func() error { _, err := p.GetJobRun(ctx, &sourcev1.GetJobRunRequest{RunId: "plugin-nope"}); return err },
	} {
		if err := call(); plugin.ErrorCode(err) != plugin.CodeNotFound {
			t.Errorf("unknown: %v", err)
		}
	}

	// The plugin's own job: it ran when configured, and runs on request.
	own, err := p.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "plugin.sync", OnBehalfOf: "alice"})
	if plugin.ErrorReason(err) == plugin.ReasonAlreadyRunning {
		own, err = waitAndRun(t, p)
	}
	if err != nil || !strings.HasPrefix(own.GetRun().GetId(), "plugin-") || own.GetRun().GetTriggeredBy() != "alice" {
		t.Fatalf("own run: %v, %v", own, err)
	}
	done := waitRun(t, p, own.GetRun().GetId())
	if done.GetSummary().GetStatus() != jobsv1.JobRunStatus_JOB_RUN_STATUS_SUCCEEDED {
		t.Fatalf("own run: %v", done)
	}
	ownRuns, _ := p.ListJobRuns(ctx, &sourcev1.ListJobRunsRequest{JobId: "plugin.sync"})
	if len(ownRuns.GetRuns()) < 2 || ownRuns.GetRuns()[len(ownRuns.GetRuns())-1].GetTrigger() != jobsv1.JobTrigger_JOB_TRIGGER_SYSTEM {
		t.Fatalf("own runs: %v", ownRuns)
	}
}

func waitAndRun(t *testing.T, p *mihon.Plugin) (*sourcev1.RunJobResponse, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := p.RunJob(context.Background(), &sourcev1.RunJobRequest{JobId: "plugin.sync", OnBehalfOf: "alice"})
		if plugin.ErrorReason(err) != plugin.ReasonAlreadyRunning || time.Now().After(deadline) {
			return res, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitRun(t *testing.T, p *mihon.Plugin, id string) *jobsv1.JobRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := p.GetJobRun(context.Background(), &sourcev1.GetJobRunRequest{RunId: id})
		if err != nil {
			t.Fatal(err)
		}
		if res.GetRun().GetSummary().GetStatus() != jobsv1.JobRunStatus_JOB_RUN_STATUS_RUNNING {
			return res.GetRun()
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still running", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOwnJobsWhenMihonSourceIsDown(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	up.srv.Close()
	list, err := p.ListJobs(context.Background(), &sourcev1.ListJobsRequest{})
	if err != nil || jobIDs(list) != "plugin.sync" {
		t.Fatalf("jobs: %v, %v", jobIDs(list), err)
	}
	if _, err := p.GetJob(context.Background(), &sourcev1.GetJobRequest{JobId: "store-refresh"}); plugin.ErrorReason(err) != plugin.ReasonSourceDown {
		t.Fatalf("a job of mihon-source: %v", err)
	}
}

func TestSourceHealthCheckSwitch(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, map[string]any{"languages": []any{"en"}}, false)
	ctx := context.Background()
	if _, err := p.SetSourceHealthCheck(ctx, &sourcev1.SetSourceHealthCheckRequest{SourceId: "1001"}); err != nil {
		t.Fatal(err)
	}
	up.admin.mu.Lock()
	off := up.admin.checksOff["1001"]
	up.admin.mu.Unlock()
	if !off {
		t.Fatal("checks weren't turned off in mihon-source")
	}
	// A source outside the plugin's languages, or none at all, isn't there.
	for _, id := range []string{"2001", "nope"} {
		if _, err := p.SetSourceHealthCheck(ctx, &sourcev1.SetSourceHealthCheckRequest{SourceId: id, Enabled: true}); plugin.ErrorCode(err) != plugin.CodeNotFound {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestHealthCheckScheduleFromTheConfiguration(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := time.Now()
	p := newPlugin(t, up, map[string]any{
		"languages":     []any{"fr", "en"},
		"health_checks": map[string]any{"interval": "6h", "sample": float64(3)},
	}, false)
	if err := p.Synced(ctx, before); err != nil {
		t.Fatal(err)
	}
	schedule := func() (*mihonv1.HealthCheckSchedule, int) {
		up.admin.mu.Lock()
		defer up.admin.mu.Unlock()
		return up.admin.schedule, up.admin.scheduleSets
	}
	s, sets := schedule()
	if s.GetInterval().AsDuration() != 6*time.Hour || strings.Join(s.GetLangs(), ",") != "en,fr" || s.GetSample() != 3 || sets != 1 {
		t.Fatalf("schedule: %v (%d sets)", s, sets)
	}

	set := func(values map[string]any) {
		t.Helper()
		values["mihon_source_url"], values["api_key"] = up.url, ""
		values["languages"] = []any{"fr", "en"}
		st, err := structpb.NewStruct(values)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now()
		if _, err := p.SetConfig(ctx, &sourcev1.SetConfigRequest{Values: st}); err != nil {
			t.Fatal(err)
		}
		if err := p.Synced(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	// The same again changes nothing.
	set(map[string]any{"health_checks": map[string]any{"interval": "6h", "sample": 3}})
	if _, sets := schedule(); sets != 1 {
		t.Fatalf("set again: %d sets", sets)
	}
	set(map[string]any{"health_checks": map[string]any{"interval": "off", "sample": 3}})
	if s, _ := schedule(); s == nil || s.GetInterval() != nil {
		t.Fatalf("off: %v", s)
	}
	set(map[string]any{"health_checks": map[string]any{"interval": "default"}})
	if s, sets := schedule(); s != nil || sets != 3 {
		t.Fatalf("default: %v (%d sets)", s, sets)
	}
	// Empty leaves mihon-source alone.
	set(map[string]any{"health_checks": map[string]any{"interval": ""}})
	if _, sets := schedule(); sets != 3 || p.StoreProblem() != "" {
		t.Fatalf("empty: %d sets, problem %q", sets, p.StoreProblem())
	}

	st, _ := structpb.NewStruct(map[string]any{"mihon_source_url": up.url, "health_checks": map[string]any{"interval": "5 hours"}})
	if _, err := p.SetConfig(ctx, &sourcev1.SetConfigRequest{Values: st}); plugin.ErrorCode(err) != plugin.CodeInvalidArgument {
		t.Fatalf("a bad interval: %v", err)
	}
}

func TestSyncJobIsDisabledWithNothingToKeep(t *testing.T) {
	up := newUpstream(t, plugintest.New())
	p := newPlugin(t, up, nil, false)
	ctx := context.Background()
	job, err := p.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "plugin.sync"})
	if err != nil || !strings.HasPrefix(job.GetJob().GetDisabledReason(), "Nothing to keep") || job.GetJob().GetNextRunAt() != nil {
		t.Fatalf("job: %v, %v", job, err)
	}
	if _, err := p.RunJob(ctx, &sourcev1.RunJobRequest{JobId: "plugin.sync"}); plugin.ErrorReason(err) != plugin.ReasonJobDisabled {
		t.Fatalf("run: %v", err)
	}

	before := time.Now()
	st, _ := structpb.NewStruct(map[string]any{"mihon_source_url": up.url, "health_checks": map[string]any{"interval": "1d"}})
	if _, err := p.SetConfig(ctx, &sourcev1.SetConfigRequest{Values: st}); err != nil {
		t.Fatal(err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.Synced(syncCtx, before); err != nil {
		t.Fatal(err)
	}
	job, _ = p.GetJob(ctx, &sourcev1.GetJobRequest{JobId: "plugin.sync"})
	if job.GetJob().GetDisabledReason() != "" || job.GetJob().GetNextRunAt() == nil || job.GetJob().GetLastRun() == nil {
		t.Fatalf("job once there's something to keep: %v", job.GetJob())
	}
}
