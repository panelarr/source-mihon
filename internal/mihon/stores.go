// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/panelarr/source-api/sdk/jobs"
	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// storeSync keeps the configuration in mihon-source: its stores and
// extensions, and its health-check schedule. It runs as the plugin's
// "plugin.sync" job: after every configuration change and then
// periodically, so a fresh or restored mihon-source catches up. It only
// adds: stores and extensions added in mihon-source by other means are left
// alone, and updates are installed there.
type storeSync struct {
	p       *Plugin
	pending atomic.Bool
	closed  atomic.Bool

	mu       sync.Mutex
	problems []string
	lastRun  time.Time
}

const syncJobID = ownJobPrefix + "sync"

func newStoreSync(p *Plugin) *storeSync { return &storeSync{p: p} }

// job is the "plugin.sync" job, every so often.
func (s *storeSync) job(every time.Duration) jobs.Job {
	return jobs.Job{
		ID:   syncJobID,
		Name: "Keep the configuration in mihon-source",
		Description: "Adds the configured stores to mihon-source, installs the configured extensions that are missing, " +
			"and applies the configured health-check schedule. Runs after every configuration change, too.",
		Group:          "Plugin",
		Every:          every,
		ScheduleSource: "source-mihon",
		Cancellable:    true,
		DisabledReason: s.disabledReason,
		Run: func(ctx context.Context, run *jobs.Run) error {
			s.pending.Store(false)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if problems := s.reconcile(ctx, run); len(problems) > 0 {
				return errors.New(strings.Join(problems, "; "))
			}
			return ctx.Err()
		},
	}
}

// disabledReason says why there's nothing to keep, or "".
func (s *storeSync) disabledReason() string {
	set := s.p.settings.Load()
	switch {
	case set == nil || s.p.up.Load() == nil:
		return "source-mihon isn't configured yet"
	case len(set.Stores) == 0 && len(set.Extensions) == 0 && set.HealthInterval == "":
		return "Nothing to keep: no stores, extensions or health-check interval are configured"
	}
	return ""
}

// kick asks for a reconcile now. If one is running, another follows it, so
// the latest configuration is always applied.
func (s *storeSync) kick() {
	if s.closed.Load() {
		return
	}
	s.pending.Store(true)
	_, err := s.p.jobs.Trigger(syncJobID, nil)
	if plugin.ErrorReason(err) == plugin.ReasonJobDisabled {
		// Nothing to keep: in step by definition, and earlier problems are moot.
		s.pending.Store(false)
		s.mu.Lock()
		s.problems, s.lastRun = nil, time.Now()
		s.mu.Unlock()
		return
	}
	var pe *plugin.Error
	if errors.As(err, &pe) && pe.Reason == plugin.ReasonAlreadyRunning {
		go func() {
			_, _ = s.p.jobs.Wait(context.Background(), pe.CurrentRunID)
			if s.pending.Load() {
				s.kick()
			}
		}()
	}
}

func (s *storeSync) stop() { s.closed.Store(true) }

// problem is what went wrong in the last reconcile, for Health; empty when
// it worked.
func (s *storeSync) problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.problems, "; ")
}

// Synced waits until a reconcile has finished after t, for tests.
func (p *Plugin) Synced(ctx context.Context, after time.Time) error {
	for {
		p.stores.mu.Lock()
		done := p.stores.lastRun.After(after)
		p.stores.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// StoreProblem is the last reconcile's problem, for tests and Health.
func (p *Plugin) StoreProblem() string { return p.stores.problem() }

// reconcile applies the configuration once and returns what went wrong.
func (s *storeSync) reconcile(ctx context.Context, run *jobs.Run) []string {
	set := s.p.settings.Load()
	u := s.p.up.Load()
	var problems []string
	if set == nil || u == nil {
		run.Skip("Not configured yet")
	} else {
		if len(set.Stores) > 0 || len(set.Extensions) > 0 {
			problems = s.reconcileWith(ctx, u, set, run)
		}
		if pr := s.syncHealthChecks(ctx, u, set); pr != "" {
			problems = append(problems, pr)
		}
		if len(problems) == 0 {
			run.SetMessage("In step with the configuration")
		}
	}
	for _, pr := range problems {
		s.p.log.Warn("keeping the configuration in mihon-source", "problem", pr)
		run.Note(jobs.LevelWarn, pr, "")
	}
	s.mu.Lock()
	s.problems = problems
	s.lastRun = time.Now()
	s.mu.Unlock()
	s.p.metrics.storeSync(len(problems) == 0)
	return problems
}

func sameURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func (s *storeSync) reconcileWith(ctx context.Context, u *upstream, set *Settings, run *jobs.Run) []string {
	var problems []string
	listed, err := call(ctx, u, func() (*connect.Response[mihonv1.ListStoresResponse], error) {
		return u.stores.ListStores(ctx, connect.NewRequest(&mihonv1.ListStoresRequest{}))
	})
	if err != nil {
		return []string{fmt.Sprintf("listing stores: %v", err)}
	}
	have := listed.Msg.GetStores()
	for _, want := range set.Stores {
		if slices.ContainsFunc(have, func(st *mihonv1.Store) bool { return sameURL(st.GetUrl(), want.URL) }) {
			continue
		}
		added, err := call(ctx, u, func() (*connect.Response[mihonv1.AddStoreResponse], error) {
			return u.stores.AddStore(ctx, connect.NewRequest(&mihonv1.AddStoreRequest{Url: want.URL, ExpectedSigningKeyFingerprint: want.Fingerprint}))
		})
		if err != nil {
			problems = append(problems, failure("store "+want.URL, err))
			continue
		}
		s.p.log.Info("store added to mihon-source", "store", added.Msg.GetStore().GetName(), "id", added.Msg.GetStore().GetId())
		run.Add("stores_added", 1)
		run.Note(jobs.LevelInfo, "Added store "+added.Msg.GetStore().GetName(), added.Msg.GetStore().GetId())
		have = append(have, added.Msg.GetStore())
	}
	if len(set.Extensions) == 0 {
		return problems
	}
	avail, err := call(ctx, u, func() (*connect.Response[mihonv1.ListAvailableResponse], error) {
		return u.stores.ListAvailable(ctx, connect.NewRequest(&mihonv1.ListAvailableRequest{}))
	})
	if err != nil {
		return append(problems, fmt.Sprintf("listing extensions: %v", err))
	}
	for _, pkg := range set.Extensions {
		offers := slices.DeleteFunc(slices.Clone(avail.Msg.GetExtensions()), func(e *mihonv1.AvailableExtension) bool { return e.GetPkgName() != pkg })
		if slices.ContainsFunc(offers, func(e *mihonv1.AvailableExtension) bool { return e.GetInstalled() }) {
			continue
		}
		pick := pickOffer(offers)
		if pick == nil {
			problems = append(problems, fmt.Sprintf("extension %s: no store offers a supported version", pkg))
			continue
		}
		installed, err := call(ctx, u, func() (*connect.Response[mihonv1.InstallFromStoreResponse], error) {
			return u.stores.InstallFromStore(ctx, connect.NewRequest(&mihonv1.InstallFromStoreRequest{StoreId: pick.GetStoreId(), PkgName: pkg}))
		})
		if err != nil {
			problems = append(problems, failure("extension "+pkg, err))
			continue
		}
		s.p.log.Info("extension installed in mihon-source", "extension", pkg, "version", installed.Msg.GetExtension().GetVersionName())
		run.Add("extensions_installed", 1)
		run.Note(jobs.LevelInfo, "Installed "+pkg+" "+installed.Msg.GetExtension().GetVersionName(), pkg)
	}
	return problems
}

// failure describes a failed store or extension call by what failed: the
// store (or a file it lists), which mihon-source reports with a reason, or
// mihon-source itself.
func failure(what string, err error) string {
	var pe *plugin.Error
	msg := err.Error()
	if errors.As(err, &pe) {
		msg = pe.Message
	}
	var a *answered
	switch {
	case plugin.ErrorReason(err) == reasonFetchFailed,
		// mihon-source versions without reasons: it answered, so the download failed.
		plugin.ErrorReason(err) == "" && errors.As(err, &a) && plugin.ErrorCode(err) == plugin.CodeUnavailable:
		return fmt.Sprintf("%s couldn't be fetched: %s", what, msg)
	case plugin.ErrorReason(err) == reasonInvalid:
		return fmt.Sprintf("%s can't be used: %s", what, msg)
	}
	return fmt.Sprintf("%s: %v", what, err)
}

// Reasons mihon-source's store calls give (domain mihon.panelarr).
const (
	reasonFetchFailed plugin.Reason = "STORE_FETCH_FAILED"
	reasonInvalid     plugin.Reason = "STORE_INVALID"
)

// pickOffer prefers a supported jar, then a supported APK, then the newest.
func pickOffer(offers []*mihonv1.AvailableExtension) *mihonv1.AvailableExtension {
	var best *mihonv1.AvailableExtension
	score := func(e *mihonv1.AvailableExtension) int64 {
		n := e.GetVersionCode()
		if e.GetHasJar() {
			n += 1 << 40
		}
		return n
	}
	for _, e := range offers {
		if !e.GetSupported() {
			continue
		}
		if best == nil || score(e) > score(best) {
			best = e
		}
	}
	return best
}

// Where mihon-source says a schedule set over its API comes from.
const scheduleFromAPI = "SetHealthCheckSchedule"

// syncHealthChecks applies health_checks to mihon-source: an interval (with
// the plugin's languages and the sample), "off", or "default" to go back to
// mihon-source's own. An empty interval leaves mihon-source alone. It
// returns a problem, or "".
func (s *storeSync) syncHealthChecks(ctx context.Context, u *upstream, set *Settings) string {
	if set.HealthInterval == "" {
		return ""
	}
	var want *mihonv1.HealthCheckSchedule
	if set.HealthInterval != "default" {
		langs := slices.Clone(set.Langs)
		slices.Sort(langs)
		want = &mihonv1.HealthCheckSchedule{Langs: langs, Sample: uint32(max(set.HealthSample, 0))} //nolint:gosec // not negative
		if every := set.healthEvery(); every > 0 {
			want.Interval = durationpb.New(every)
		}
	}
	cur, err := call(ctx, u, func() (*connect.Response[mihonv1.GetHealthCheckScheduleResponse], error) {
		return u.extensions.GetHealthCheckSchedule(ctx, connect.NewRequest(&mihonv1.GetHealthCheckScheduleRequest{}))
	})
	if plugin.ErrorCode(err) == plugin.CodeUnimplemented {
		return "health checks: this mihon-source can't take a schedule; update it, or leave health_checks.interval empty"
	}
	if err != nil {
		return fmt.Sprintf("health checks: %v", u.mapError(err))
	}
	have := proto.CloneOf(cur.Msg.GetSchedule())
	fromAPI := have.GetSource() == scheduleFromAPI
	have.Source = ""
	if (want == nil && !fromAPI) || (want != nil && fromAPI && proto.Equal(have, want)) {
		return ""
	}
	_, err = call(ctx, u, func() (*connect.Response[mihonv1.SetHealthCheckScheduleResponse], error) {
		return u.extensions.SetHealthCheckSchedule(ctx, connect.NewRequest(&mihonv1.SetHealthCheckScheduleRequest{Schedule: want}))
	})
	if err != nil {
		return fmt.Sprintf("health checks: %v", u.mapError(err))
	}
	s.p.log.Info("health-check schedule set in mihon-source", "interval", set.HealthInterval, "sample", set.HealthSample, "langs", set.Langs)
	return ""
}
