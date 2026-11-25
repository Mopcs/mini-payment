package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Mopcs/mini-payment-system/internal/observability"
)

const metricsPath = "/metrics"

// Tracing opens a server span for every request except the metrics endpoint.
// The span records the route pattern rather than the raw path, so wallet and
// transfer identifiers never end up in a trace.
func Tracing(next http.Handler) http.Handler {
	tracer := observability.Tracer()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == metricsPath {
			next.ServeHTTP(w, r)
			return
		}

		response := observability.NewStatusRecorder(w)
		request := r.WithContext(extractRemoteContext(r))

		ctx, span := tracer.Start(
			request.Context(),
			"HTTP "+r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(semconv.HTTPRequestMethodKey.String(r.Method)),
		)
		request = request.WithContext(ctx)
		defer endServerSpan(span, request, response)

		next.ServeHTTP(response, request)
	})
}

// extractRemoteContext pulls the W3C trace context out of the incoming headers.
// Without it every request would start a new trace instead of continuing the
// caller's.
func extractRemoteContext(r *http.Request) context.Context {
	carrier := propagation.HeaderCarrier(r.Header)
	return otel.GetTextMapPropagator().Extract(r.Context(), carrier)
}

// spanName keeps the method prefix ServeMux already puts in the pattern.
func spanName(method, pattern string) string {
	if strings.HasPrefix(pattern, method+" ") {
		return pattern
	}
	return method + " " + pattern
}

// routePath drops the method prefix, because http.route is a path only.
func routePath(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

func endServerSpan(
	span trace.Span,
	request *http.Request,
	response *observability.StatusRecorder,
) {
	recovered := recover()
	if recovered != nil {
		response.Fail(http.StatusInternalServerError)
		span.RecordError(fmt.Errorf("handler panic: %v", recovered))
	}

	// ServeMux fills Pattern while routing, so the route is only known here.
	pattern := request.Pattern
	if pattern == "" {
		pattern = "unmatched"
	}
	span.SetName(spanName(request.Method, pattern))
	span.SetAttributes(
		semconv.HTTPRouteKey.String(routePath(pattern)),
		semconv.HTTPResponseStatusCodeKey.Int(response.Status()),
	)

	if response.Status() >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, http.StatusText(response.Status()))
	}
	span.End()

	if recovered != nil {
		panic(recovered)
	}
}
