// Package shutdown coordinates a bounded, graceful stop for the executables.
// Every binary needs the same three things: a context cancelled by SIGTERM or
// SIGINT, a budget for finishing in-flight work, and a hard stop when that
// budget runs out. Keeping them here means the contract cannot drift between
// the API, the outbox worker and the consumer.
package shutdown

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultBudget is used when the caller has no configured timeout.
const DefaultBudget = 20 * time.Second

// exitCodeTimeout is returned when the graceful stop overruns its budget.
const exitCodeTimeout = 1

// NotifyContext returns a context that is cancelled by SIGINT or SIGTERM. The
// signal handler is released as soon as the first signal arrives, so a second
// one terminates the process immediately instead of being swallowed while a
// stuck shutdown runs.
func NotifyContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// Watchdog enforces the shutdown budget. When a graceful stop overruns it, the
// process exits instead of hanging, which keeps the orchestrator from
// escalating to SIGKILL and losing buffered telemetry.
type Watchdog struct {
	budget time.Duration
	logger *slog.Logger
	// signalled records that the stop was requested by a signal, so the
	// completion line is not printed on a plain error exit.
	signalled atomic.Bool
}

// NewWatchdog returns a watchdog that waits at most budget after a signal.
func NewWatchdog(budget time.Duration, logger *slog.Logger) *Watchdog {
	if budget <= 0 {
		budget = DefaultBudget
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watchdog{budget: budget, logger: logger}
}

// Start watches ctx. The returned function reports that the graceful stop
// finished and should be deferred by the caller.
func (w *Watchdog) Start(ctx context.Context) func() {
	done := make(chan struct{})
	var once sync.Once

	// The completion line is written here, not in the watcher goroutine: the
	// process exits as soon as run returns, so a log emitted from another
	// goroutine can be lost.
	finish := func() {
		once.Do(func() {
			close(done)
			if w.signalled.Load() {
				w.logger.Info("graceful shutdown complete")
			}
		})
	}

	go w.watch(ctx, done)
	return finish
}

func (w *Watchdog) watch(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-done:
		return
	}

	w.signalled.Store(true)
	w.logger.Info("shutdown signal received", slog.String("budget", w.budget.String()))
	timer := time.NewTimer(w.budget)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
		// A stop that finished just as the timer fired is not a timeout.
		select {
		case <-done:
			return
		default:
		}
		w.logger.Error("graceful shutdown exceeded its budget",
			slog.String("budget", w.budget.String()))
		os.Exit(exitCodeTimeout)
	}
}
