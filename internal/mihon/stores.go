// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/panelarr/source-api/sdk/plugin"

	mihonv1 "github.com/panelarr/source-mihon/gen/panelarr/mihon/v1"
)

// storeSync keeps the stores and extensions of the configuration in
// mihon-source: after every configuration change and then periodically, so
// a fresh or restored mihon-source catches up. It only adds: stores and
// extensions added in mihon-source by other means are left alone, and
// updates are installed there.
type storeSync struct {
	p     *Plugin
	every time.Duration
	kickc chan struct{}
	stopc chan struct{}
	once  sync.Once
	start sync.Once
	wg    sync.WaitGroup

	mu       sync.Mutex
	problems []string
	lastRun  time.Time
}

func newStoreSync(p *Plugin, every time.Duration) *storeSync {
	return &storeSync{p: p, every: every, kickc: make(chan struct{}, 1), stopc: make(chan struct{})}
}

// kick asks for a reconcile now, starting the loop on first use.
func (s *storeSync) kick() {
	s.start.Do(func() {
		s.wg.Add(1)
		go s.loop()
	})
	select {
	case s.kickc <- struct{}{}:
	default:
	}
}

func (s *storeSync) stop() {
	s.once.Do(func() { close(s.stopc) })
	s.wg.Wait()
}

func (s *storeSync) loop() {
	defer s.wg.Done()
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-s.stopc:
			return
		case <-s.kickc:
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			select {
			case <-s.stopc:
				cancel()
			case <-ctx.Done():
			}
		}()
		s.reconcile(ctx)
		cancel()
	}
}

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

func (s *storeSync) reconcile(ctx context.Context) {
	set := s.p.settings.Load()
	u := s.p.up.Load()
	var problems []string
	if set != nil && u != nil && (len(set.Stores) > 0 || len(set.Extensions) > 0) {
		problems = s.reconcileWith(ctx, u, set)
	}
	for _, pr := range problems {
		s.p.log.Warn("keeping stores and extensions", "problem", pr)
	}
	s.mu.Lock()
	s.problems = problems
	s.lastRun = time.Now()
	s.mu.Unlock()
	s.p.metrics.storeSync(len(problems) == 0)
}

func sameURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func (s *storeSync) reconcileWith(ctx context.Context, u *upstream, set *Settings) []string {
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
