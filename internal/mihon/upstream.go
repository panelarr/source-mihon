// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/panelarr/source-api/sdk/client"
	"github.com/panelarr/source-api/sdk/plugin"

	"github.com/panelarr/source-mihon/gen/panelarr/mihon/v1/mihonv1connect"
)

// upstream is one mihon-source: its source API, its store and extension
// admin APIs, and plain HTTP for icons. All calls speak gRPC with the API
// key, so any mihon-source version with the same protos works.
type upstream struct {
	url        string
	key        string
	http       *http.Client
	source     client.Client
	stores     mihonv1connect.MihonStoresServiceClient
	extensions mihonv1connect.MihonAdminServiceClient
}

func newUpstream(s Settings) *upstream {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP1(true)
	tr.Protocols.SetHTTP2(true)
	tr.Protocols.SetUnencryptedHTTP2(true)
	tr.ResponseHeaderTimeout = 2 * time.Minute
	hc := &http.Client{Transport: tr}
	opts := []connect.ClientOption{
		connect.WithGRPC(),
		connect.WithReadMaxBytes(plugin.MaxMessageSize),
		connect.WithSendMaxBytes(plugin.MaxMessageSize),
	}
	if s.APIKey != "" {
		opts = append(opts, connect.WithInterceptors(bearer(s.APIKey)))
	}
	return &upstream{
		url:        s.URL,
		key:        s.APIKey,
		http:       hc,
		source:     client.Remote(s.URL, client.RemoteOptions{APIKey: s.APIKey, HTTPClient: hc}),
		stores:     mihonv1connect.NewMihonStoresServiceClient(hc, s.URL, opts...),
		extensions: mihonv1connect.NewMihonAdminServiceClient(hc, s.URL, opts...),
	}
}

func (u *upstream) close() {
	_ = u.source.Close()
	u.http.CloseIdleConnections()
}

// bearer sends the API key on every call.
type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(b))
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(b))
		return conn
	}
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// retries is how often a call is retried when mihon-source can't be reached.
var retryDelays = []time.Duration{200 * time.Millisecond, 800 * time.Millisecond}

// answered is an error mihon-source answered with. It was reachable, so the
// failure is about the request (e.g. a store it couldn't fetch), not about
// mihon-source.
type answered struct{ err *plugin.Error }

func (a *answered) Error() string { return a.err.Error() }

func (a *answered) Unwrap() error { return a.err }

// unreachable reports whether err means mihon-source itself couldn't be
// reached (as opposed to a source failing behind it, which carries a reason,
// or anything mihon-source answered).
func unreachable(err error) bool {
	var a *answered
	if errors.As(err, &a) {
		return false
	}
	code := plugin.ErrorCode(err)
	return (code == plugin.CodeUnavailable || code == connect.CodeUnknown) && plugin.ErrorReason(err) == ""
}

// mapError explains failures that are about mihon-source rather than a
// source: unreachable, or a key it doesn't accept. Errors of sources pass
// through with their code, reason and retry hint.
func (u *upstream) mapError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	switch {
	case unreachable(err):
		return plugin.ErrSourceDown("mihon-source at %s can't be reached: %v", u.url, err)
	case plugin.ErrorCode(err) == plugin.CodeUnauthenticated && plugin.ErrorReason(err) == "":
		return plugin.Errorf(plugin.CodeFailedPrecondition, "mihon-source rejected the API key; check the plugin's api_key")
	case plugin.ErrorCode(err) == plugin.CodePermissionDenied && plugin.ErrorReason(err) == "":
		return plugin.Errorf(plugin.CodeFailedPrecondition, "the API key lacks a scope mihon-source needs for this call: %v", err)
	}
	return err
}

// call runs f against mihon-source, retrying while mihon-source can't be
// reached (every source call is safe to repeat).
func call[T any](ctx context.Context, u *upstream, f func() (T, error)) (T, error) {
	// The admin clients return Connect errors, which tell whether mihon-source
	// answered; the source client already returns *plugin.Error.
	try := func() (T, error) {
		res, err := f()
		var ce *connect.Error
		if errors.As(err, &ce) && connect.IsWireError(ce) {
			if pe, ok := plugin.FromConnectError(err).(*plugin.Error); ok {
				return res, &answered{err: pe}
			}
		}
		return res, plugin.FromConnectError(err)
	}
	res, err := try()
	for _, d := range retryDelays {
		if err == nil || !unreachable(err) {
			break
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(d):
		}
		res, err = try()
	}
	if err != nil {
		var zero T
		return zero, u.mapError(err)
	}
	return res, nil
}
