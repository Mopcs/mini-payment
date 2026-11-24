package observability

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPGXPoolCollectorReportsPoolStatistics(t *testing.T) {
	config, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	registry := prometheus.NewRegistry()
	if err := registry.Register(NewPGXPoolCollector(pool)); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetricFamily(families, "payment_postgres_pool_connections") {
		t.Fatal("pool connection metrics are missing")
	}
	if !hasMetricFamily(families, "payment_postgres_pool_acquire_total") {
		t.Fatal("pool acquisition metrics are missing")
	}
	if !hasMetricFamily(families, "payment_postgres_pool_acquire_duration_seconds_total") {
		t.Fatal("pool acquisition duration metric is missing")
	}
	if !hasMetricFamily(families, "payment_postgres_pool_empty_acquire_total") {
		t.Fatal("empty acquisition metric is missing")
	}
}

func hasMetricFamily(families []*dto.MetricFamily, name string) bool {
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}
