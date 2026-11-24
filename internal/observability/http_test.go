package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestHTTPMetricsUseRoutePatternAndFinalStatus(t *testing.T) {
	registry := NewRegistry()
	metrics := NewHTTPMetrics(registry)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wallets/{walletID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	handler := metrics.Instrument(mux)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/wallets/secret-id", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	assertCounter(t, metrics.RequestsTotal.WithLabelValues("GET", "GET /wallets/{walletID}", "201"), 1)
	assertHistogramCount(t, registry, "payment_http_request_duration_seconds", 1)
	assertGauge(t, metrics.RequestsActive.WithLabelValues("GET"), 0)
	assertMetricsEndpoint(t, handler, registry)
}

func assertMetricsEndpoint(t *testing.T, handler http.Handler, registry *prometheus.Registry) {
	t.Helper()
	metricsResponse := httptest.NewRecorder()
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", metricsResponse.Code)
	}
	exposition := metricsResponse.Body.String()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(exposition))
	if err != nil {
		t.Fatalf("parse Prometheus exposition: %v", err)
	}
	if _, ok := families["payment_http_requests_total"]; !ok {
		t.Fatal("request counter is missing from metrics exposition")
	}
	if strings.Contains(exposition, `route="GET /metrics"`) {
		t.Fatal("metrics endpoint was included in HTTP request metrics")
	}
	if strings.Contains(exposition, "secret-id") {
		t.Fatal("raw wallet IDs were included in metric labels")
	}
}

func assertCounter(t *testing.T, counter prometheus.Counter, want float64) {
	t.Helper()
	if got := metricValue(t, counter); got != want {
		t.Fatalf("counter = %v, want %v", got, want)
	}
}

func assertGauge(t *testing.T, gauge prometheus.Gauge, want float64) {
	t.Helper()
	if got := metricValue(t, gauge); got != want {
		t.Fatalf("gauge = %v, want %v", got, want)
	}
}

func metricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	data := &dto.Metric{}
	if err := metric.Write(data); err != nil {
		t.Fatal(err)
	}
	if data.Counter != nil {
		return data.Counter.GetValue()
	}
	return data.Gauge.GetValue()
}

func assertHistogramCount(t *testing.T, registry *prometheus.Registry, name string, want uint64) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		if got := family.Metric[0].GetHistogram().GetSampleCount(); got != want {
			t.Fatalf("histogram count = %d, want %d", got, want)
		}
		return
	}
	t.Fatalf("histogram %q was not registered", name)
}

func TestHTTPMetricsRecordPanicAsServerError(t *testing.T) {
	registry := NewRegistry()
	metrics := NewHTTPMetrics(registry)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) {
		panic("handler failed")
	})
	handler := recoverHTTP(metrics.Instrument(mux))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic response status = %d", response.Code)
	}
	assertCounter(t, metrics.RequestsTotal.WithLabelValues("GET", "GET /panic", "500"), 1)
}

func recoverHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
