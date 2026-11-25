package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type HTTPMetrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	RequestsActive  *prometheus.GaugeVec
}

func NewHTTPMetrics(registry *prometheus.Registry) *HTTPMetrics {
	metrics := &HTTPMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payment_http_requests_total",
			Help: "Total number of completed HTTP requests.",
		}, []string{"method", "route", "status_code"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "payment_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status_code"}),
		RequestsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "payment_http_requests_in_progress",
			Help: "Number of HTTP requests currently being handled.",
		}, []string{"method"}),
	}
	registry.MustRegister(metrics.RequestsTotal, metrics.RequestDuration, metrics.RequestsActive)
	return metrics
}

func (m *HTTPMetrics) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		started := time.Now()
		response := NewStatusRecorder(w)
		method := normalizeMethod(r.Method)
		m.RequestsActive.WithLabelValues(method).Inc()
		defer m.observeRequest(response, r, method, started)
		next.ServeHTTP(response, r)
	})
}

func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace,
		http.MethodConnect:
		return method
	default:
		return "OTHER"
	}
}

// observeRequest runs deferred so it also sees a handler panic. The request is
// recorded either way and the panic keeps travelling up the stack.
func (m *HTTPMetrics) observeRequest(
	response *StatusRecorder,
	request *http.Request,
	method string,
	started time.Time,
) {
	recovered := recover()
	if recovered != nil {
		response.Fail(http.StatusInternalServerError)
	}

	m.recordRequest(response, request, method, started)
	if recovered != nil {
		panic(recovered)
	}
}

func (m *HTTPMetrics) recordRequest(
	response *StatusRecorder,
	request *http.Request,
	method string,
	started time.Time,
) {
	route := request.Pattern
	if route == "" {
		route = "unmatched"
	}
	m.RequestsActive.WithLabelValues(method).Dec()
	status := strconv.Itoa(response.Status())
	labels := []string{method, route, status}
	m.RequestsTotal.WithLabelValues(labels...).Inc()
	m.RequestDuration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
}
