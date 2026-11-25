package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	remoteTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	remoteSpanID  = "00f067aa0ba902b7"
	walletID      = "3f1c8c1e-8d3e-4f0a-9b2f-5f7e2b9a1c44"
	walletPath    = "/api/v1/wallets/" + walletID
)

// testExporter installs a provider that exports spans synchronously, so the
// assertions can read them right after the request returns.
func testExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return exporter
}

func attributeValue(attributes []attribute.KeyValue, key string) (string, bool) {
	for _, attr := range attributes {
		if string(attr.Key) == key {
			return attr.Value.Emit(), true
		}
	}
	return "", false
}

func TestTracingContinuesRemoteTraceAndRecordsRoute(t *testing.T) {
	exporter := testExporter(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/wallets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, walletPath, nil)
	request.Header.Set("traceparent", "00-"+remoteTraceID+"-"+remoteSpanID+"-01")
	Tracing(mux).ServeHTTP(httptest.NewRecorder(), request)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	span := spans[0]

	if got := span.SpanContext.TraceID().String(); got != remoteTraceID {
		t.Fatalf("trace ID = %s, want the caller trace %s", got, remoteTraceID)
	}
	if want := "GET /api/v1/wallets/{id}"; span.Name != want {
		t.Fatalf("span name = %q, want %q", span.Name, want)
	}

	route, ok := attributeValue(span.Attributes, "http.route")
	if !ok || route != "/api/v1/wallets/{id}" {
		t.Fatalf("http.route = %q (present=%t)", route, ok)
	}
	if _, ok := attributeValue(span.Attributes, "url.path"); ok {
		t.Fatal("url.path must not be recorded, it carries identifiers")
	}

	for _, attr := range span.Attributes {
		if strings.Contains(attr.Value.Emit(), walletID) {
			t.Fatalf("attribute %q leaks the wallet identifier", attr.Key)
		}
	}
}

func TestTracingMarksServerErrors(t *testing.T) {
	exporter := testExporter(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /failing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	Tracing(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/failing", nil))

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("span status = %v, want %v", spans[0].Status.Code, codes.Error)
	}
	if status, _ := attributeValue(spans[0].Attributes, "http.response.status_code"); status != "500" {
		t.Fatalf("http.response.status_code = %q, want 500", status)
	}
}

func TestTracingSkipsMetricsEndpoint(t *testing.T) {
	exporter := testExporter(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	Tracing(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if spans := exporter.GetSpans(); len(spans) != 0 {
		t.Fatalf("recorded %d spans for the metrics endpoint, want 0", len(spans))
	}
}
