// Package metrics defines all Prometheus metrics for TrialTerminator.
// Every metric lives here — no prometheus.NewCounter calls scattered across handlers.
//
// ARCHITECTURAL DECISION: Why centralise metrics in one package?
// If metrics are defined where they're used (inside handlers, inside repos),
// you end up with duplicate metric names causing panics at startup, or
// metrics that are impossible to find when debugging an alert at 3am.
// One package = one place to audit every number the system emits.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// promauto registers metrics automatically on the default Prometheus registry.
// The alternative is prometheus.MustRegister(...) in an init() function —
// promauto is cleaner because the metric is registered at declaration time,
// making it impossible to forget to register it.

// TasksCreatedTotal counts every call to POST /tasks that creates a NEW task.
// Label: service_name — lets you see "Netflix tasks vs Spotify tasks" separately.
// This is the RATE metric in RED: requests per second per service.
var TasksCreatedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "trialterminator_tasks_created_total",
		Help: "Total number of new tasks created, by service.",
	},
	[]string{"service_name"},
)

// TasksCompletedTotal counts tasks that reached a terminal state (SUCCESS or DEAD_LETTER).
// Labels: service_name + status — lets you build a "success vs failure" chart.
// This is the ERRORS metric in RED: track DEAD_LETTER rate to catch automation breakage.
var TasksCompletedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "trialterminator_tasks_completed_total",
		Help: "Total number of tasks reaching a terminal state, by service and status.",
	},
	[]string{"service_name", "status"},
)

// TasksInProgress tracks how many tasks are currently being processed by workers.
// This is a Gauge, not a Counter — it goes up when a worker claims a task,
// down when it finishes. A Gauge that only goes up is a sign of a stuck worker.
//
// ARCHITECTURAL DECISION: Why a Gauge here instead of querying Postgres?
// You could query `SELECT COUNT(*) FROM tasks WHERE status='IN_PROGRESS'`
// in a background goroutine and expose it as a metric. But that adds DB load.
// Incrementing/decrementing in the handler is O(1) and has zero DB cost.
// The trade-off: if the API crashes, the gauge resets to 0. That's acceptable —
// Prometheus will show a gap and the chart will reflect reality after restart.
var TasksInProgress = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "trialterminator_tasks_in_progress",
		Help: "Current number of tasks in IN_PROGRESS state.",
	},
)

// HTTPRequestDuration measures how long each API request takes.
// This is a Histogram — it tracks the distribution of values, not just the total.
// With a histogram you can compute percentiles: p50, p95, p99.
//
// ARCHITECTURAL DECISION: Why a Histogram over a Summary?
// Summaries compute percentiles in the application (high memory cost, can't aggregate).
// Histograms compute percentiles in Prometheus queries (cheap, aggregatable across instances).
// At Shopify/Uber scale, you always use Histograms.
//
// Labels: method + path + status_code — lets you see "GET /tasks P99 latency for 200s vs 404s"
var HTTPRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "trialterminator_http_request_duration_seconds",
		Help: "HTTP request latency distribution.",
		// Buckets define the histogram boundaries in seconds.
		// DefBuckets = [.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10]
		// This covers everything from 5ms to 10s — right for an API server.
		Buckets: prometheus.DefBuckets,
	},
	[]string{"method", "path", "status_code"},
)

// --- HTTP Middleware ---

// statusRecorder wraps http.ResponseWriter to capture the status code written.
// http.ResponseWriter doesn't expose the status code after WriteHeader is called,
// so we have to intercept it ourselves.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// InstrumentHandler wraps an HTTP handler and records its latency + status code.
//
// Usage in server.go:
//   mux.HandleFunc("POST /tasks", metrics.InstrumentHandler("POST /tasks", handler.CreateTask))
//
// The path label uses the PATTERN string (e.g. "GET /tasks/{id}"), not the actual
// URL (e.g. "GET /tasks/abc-123"). This is critical — using actual URLs would create
// a new metric label per unique task ID, causing a "cardinality explosion" that
// crashes Prometheus by exhausting its memory.
func InstrumentHandler(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		start := time.Now()

		next(recorder, r)

		HTTPRequestDuration.WithLabelValues(
			r.Method,
			path,
			strconv.Itoa(recorder.statusCode),
		).Observe(time.Since(start).Seconds())
	}
}
