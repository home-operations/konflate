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
	// Forge read-polling health (see syncTracker): list failures by reason,
	// whether the last list hit a rate limit, and when that limit resets — so an
	// operator can alert before the limit bites rather than find it in the logs.
	listErrors     *prometheus.CounterVec // reason: rate_limited|error
	rateLimited    prometheus.Gauge
	rateLimitReset prometheus.Gauge
	// checksSkipped counts CI-checks passes skipped to stay within the forge rate
	// budget (see checksWouldExhaustBudget). A rising count on an unauthenticated
	// instance is the signal that a token would let the CI pills stay current.
	checksSkipped prometheus.Counter
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
		listErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "forge_list_errors_total",
			Help: "Failed attempts to list PRs from the forge, by reason (rate_limited|error).",
		}, []string{"reason"}),
		rateLimited: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "forge_rate_limited",
			Help: "1 when the last forge PR-list attempt hit a rate limit, else 0.",
		}),
		rateLimitReset: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace, Name: "forge_rate_limit_reset_timestamp_seconds",
			Help: "Unix time the forge rate limit resets (0 when not rate-limited or unknown).",
		}),
		checksSkipped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricNamespace, Name: "forge_checks_skipped_total",
			Help: "CI-checks passes skipped to preserve the forge rate budget.",
		}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.diffTotal, m.diffDuration, m.queueDepth, m.prsKnown, m.httpReqs,
		m.listErrors, m.rateLimited, m.rateLimitReset, m.checksSkipped,
	)
	return m
}

// handler returns the /metrics handler for this registry.
func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
