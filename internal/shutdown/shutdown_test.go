package shutdown

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer collects log output written from the watchdog goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger(buffer *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, nil))
}

func waitForLog(t *testing.T, buffer *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log never contained %q, got: %s", want, buffer.String())
}

func TestWatchdogUsesDefaultBudgetWhenUnset(t *testing.T) {
	watchdog := NewWatchdog(0, nil)
	if watchdog.budget != DefaultBudget {
		t.Fatalf("budget = %s, want %s", watchdog.budget, DefaultBudget)
	}
	if watchdog.logger == nil {
		t.Fatal("watchdog needs a logger")
	}
}

func TestWatchdogLogsCompletionWhenStopFinishes(t *testing.T) {
	buffer := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	finish := NewWatchdog(5*time.Second, testLogger(buffer)).Start(ctx)

	cancel()
	waitForLog(t, buffer, "shutdown signal received")
	finish()

	waitForLog(t, buffer, "graceful shutdown complete")
}

// finish must be safe to call twice: run() defers it and the error path may
// also call it.
func TestWatchdogFinishIsIdempotent(t *testing.T) {
	buffer := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	finish := NewWatchdog(time.Second, testLogger(buffer)).Start(ctx)

	cancel()
	waitForLog(t, buffer, "shutdown signal received")
	finish()
	finish()
	waitForLog(t, buffer, "graceful shutdown complete")
}

// The budget exists to stop a hung shutdown. The exit path can only be observed
// from a separate process, so the test re-executes itself.
func TestWatchdogExitsWhenBudgetExpires(t *testing.T) {
	if os.Getenv("SHUTDOWN_WATCHDOG_CHILD") == "1" {
		runHungChild()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestWatchdogExitsWhenBudgetExpires")
	cmd.Env = append(os.Environ(), "SHUTDOWN_WATCHDOG_CHILD=1")
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("watchdog did not exit, got %v", err)
	}
	if exitErr.ExitCode() != exitCodeTimeout {
		t.Fatalf("exit code = %d, want %d", exitErr.ExitCode(), exitCodeTimeout)
	}
}

// runHungChild simulates a shutdown that never completes.
func runHungChild() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewWatchdog(50*time.Millisecond, slog.New(slog.NewTextHandler(os.Stderr, nil))).Start(ctx)
	time.Sleep(5 * time.Second)
	os.Exit(0)
}
