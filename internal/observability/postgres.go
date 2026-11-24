package observability

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type PGXPoolCollector struct {
	pool         *pgxpool.Pool
	connections  *prometheus.Desc
	acquireCount *prometheus.Desc
	acquireTime  *prometheus.Desc
	emptyAcquire *prometheus.Desc
}

func NewPGXPoolCollector(pool *pgxpool.Pool) *PGXPoolCollector {
	return &PGXPoolCollector{
		pool: pool,
		connections: prometheus.NewDesc(
			"payment_postgres_pool_connections",
			"PostgreSQL pool connections by state.",
			[]string{"state"}, nil,
		),
		acquireCount: prometheus.NewDesc(
			"payment_postgres_pool_acquire_total",
			"Total number of PostgreSQL pool acquire operations.", nil, nil,
		),
		acquireTime: prometheus.NewDesc(
			"payment_postgres_pool_acquire_duration_seconds_total",
			"Cumulative time spent acquiring PostgreSQL connections.", nil, nil,
		),
		emptyAcquire: prometheus.NewDesc(
			"payment_postgres_pool_empty_acquire_total",
			"Total pool acquire operations that waited for a connection.", nil, nil,
		),
	}
}

func (c *PGXPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connections
	ch <- c.acquireCount
	ch <- c.acquireTime
	ch <- c.emptyAcquire
}

func (c *PGXPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.pool.Stat()
	connectionStates := []struct {
		name  string
		value int32
	}{
		{name: "total", value: stats.TotalConns()},
		{name: "acquired", value: stats.AcquiredConns()},
		{name: "idle", value: stats.IdleConns()},
		{name: "constructing", value: stats.ConstructingConns()},
	}
	for _, state := range connectionStates {
		ch <- prometheus.MustNewConstMetric(
			c.connections, prometheus.GaugeValue, float64(state.value), state.name,
		)
	}
	ch <- prometheus.MustNewConstMetric(
		c.acquireCount, prometheus.CounterValue, float64(stats.AcquireCount()),
	)
	ch <- prometheus.MustNewConstMetric(
		c.acquireTime,
		prometheus.CounterValue,
		stats.AcquireDuration().Seconds(),
	)
	ch <- prometheus.MustNewConstMetric(
		c.emptyAcquire, prometheus.CounterValue, float64(stats.EmptyAcquireCount()),
	)
}
