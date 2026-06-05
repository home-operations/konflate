package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds konflate's Prometheus collectors on a private registry. The
// registry is served on the separate operational port (never the main, possibly
// public-facing one), so operational detail is not exposed alongside the UI.
type metrics struct {
	reg          *prometheus.Registry
	diffTotal    *prometheus.CounterVec // result: success|error
	diffDuration prometheus.Histogram
	queueDepth   prometheus.Gauge
	prsKnown     prometheus.Gauge
	httpReqs     *prometheus.CounterVec // code: 2xx|4xx|5xx
}

// metricNamespace prefixes every konflate metric name.
const metricNamespace = "konflate"

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		reg: reg,
		diffTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "diff_jobs_total",
			Help: "Total diff jobs completed, by result.",
		}, []string{"result"}),
		diffDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricNamespace, Name: "diff_duration_seconds",
			Help:    "Wall-clock duration of a PR diff render (clone + two flate renders).",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "diff_queue_depth",
			Help: "PRs currently queued or rendering.",
		}),
		prsKnown: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "pull_requests",
			Help: "Open pull requests currently tracked.",
		}),
		httpReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "http_requests_total",
			Help: "HTTP requests served by the main server, by status class.",
		}, []string{"code"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.diffTotal, m.diffDuration, m.queueDepth, m.prsKnown, m.httpReqs,
	)
	return m
}

// handler returns the /metrics handler for this registry.
func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
