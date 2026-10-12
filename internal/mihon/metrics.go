// SPDX-License-Identifier: GPL-3.0-or-later

package mihon

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/panelarr/source-api/sdk/plugin"
)

// Metrics are the plugin's Prometheus metrics. Labels are bounded: the RPC,
// the status code and the error reason.
type Metrics struct {
	reg      *prometheus.Registry
	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	up       prometheus.Gauge
	nSources prometheus.Gauge
	syncs    *prometheus.CounterVec
	pkgOps   *prometheus.CounterVec
}

// NewMetrics registers the metrics on a registry of their own, with the Go
// runtime and process collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "source_mihon_calls_total",
			Help: "Source API calls passed to mihon-source, by RPC, status code and error reason.",
		}, []string{"method", "code", "reason"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "source_mihon_call_duration_seconds",
			Help:    "How long source API calls took, mihon-source and the site included.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"method"}),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "source_mihon_upstream_up",
			Help: "Whether mihon-source answered the last health check: 1 or 0.",
		}),
		nSources: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "source_mihon_sources",
			Help: "Sources mihon-source served in the last full listing.",
		}),
		syncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "source_mihon_store_syncs_total",
			Help: "Reconciles of the configured stores and extensions, by outcome.",
		}, []string{"outcome"}),
		pkgOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "source_mihon_package_ops_total",
			Help: "Package calls (list, install, update, uninstall, languages), by outcome: ok or the status code.",
		}, []string{"op", "outcome"}),
	}
	m.reg.MustRegister(m.calls, m.duration, m.up, m.nSources, m.syncs, m.pkgOps,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) call(method string, err error, took time.Duration) {
	code := "ok"
	if err != nil {
		code = plugin.ErrorCode(err).String()
	}
	m.calls.WithLabelValues(method, code, string(plugin.ErrorReason(err))).Inc()
	m.duration.WithLabelValues(method).Observe(took.Seconds())
}

func (m *Metrics) upstreamUp(ok bool) {
	if ok {
		m.up.Set(1)
	} else {
		m.up.Set(0)
	}
}

func (m *Metrics) sources(n int, lang string) {
	if lang == "" {
		m.nSources.Set(float64(n))
	}
}

func (m *Metrics) storeSync(ok bool) {
	if ok {
		m.syncs.WithLabelValues("ok").Inc()
	} else {
		m.syncs.WithLabelValues("error").Inc()
	}
}

func (m *Metrics) packageOp(op string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = plugin.ErrorCode(err).String()
	}
	m.pkgOps.WithLabelValues(op, outcome).Inc()
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
