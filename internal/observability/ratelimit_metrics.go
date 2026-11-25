package observability

import "github.com/prometheus/client_golang/prometheus"

// Outcomes recorded by the rate limiter. They are the only label values, so the
// series count stays fixed.
const (
	RateLimitAllowed     = "allowed"
	RateLimitRejected    = "rejected"
	RateLimitUnavailable = "unavailable"
)

// RateLimitMetrics counts rate limit decisions per policy.
type RateLimitMetrics struct {
	Decisions *prometheus.CounterVec
}

func NewRateLimitMetrics(registry *prometheus.Registry) *RateLimitMetrics {
	decisions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "payment_ratelimit_decisions_total",
		Help: "Rate limit decisions by policy and outcome.",
	}, []string{"policy", "outcome"})
	registry.MustRegister(decisions)

	return &RateLimitMetrics{Decisions: decisions}
}

// Record is nil-safe, so an API without metrics keeps working.
func (m *RateLimitMetrics) Record(policy, outcome string) {
	if m == nil {
		return
	}
	m.Decisions.WithLabelValues(policy, outcome).Inc()
}
