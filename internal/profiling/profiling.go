// Package profiling serves the Go runtime profiles on a listener of their own.
//
// Profiling is off unless it is switched on, and it only ever binds a loopback
// address: a profile exposes heap contents and goroutine stacks, which is not
// something to serve to another host. It is deliberately a separate listener
// from the API, so no public route can ever reach it.
package profiling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// Sampling settings. The mutex profile records one in every ten contention
// events; the block profile records waits longer than ten microseconds. Both
// cost something at runtime, so they are only switched on together with the
// listener and never during normal operation.
const (
	mutexProfileFraction = 10
	blockProfileRate     = 10000

	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Server is the profiling listener.
type Server struct {
	server *http.Server
	addr   net.Addr
	logger *slog.Logger
}

// Addr reports the bound address. It is useful when the port is chosen by the
// operating system, as in tests.
func (s *Server) Addr() net.Addr {
	return s.addr
}

// Start binds addr and serves /debug/pprof on it. It refuses any address that
// is not loopback, before it starts serving anything.
func Start(addr string, logger *slog.Logger) (*Server, error) {
	if addr == "" {
		return nil, errors.New("profiling address is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen for profiles: %w", err)
	}
	if err := requireLoopback(listener.Addr()); err != nil {
		_ = listener.Close()
		return nil, err
	}

	runtime.SetMutexProfileFraction(mutexProfileFraction)
	runtime.SetBlockProfileRate(blockProfileRate)

	server := &http.Server{
		Handler:           routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	go serve(server, listener, logger)

	logger.Warn("profiling server started",
		slog.String("addr", listener.Addr().String()),
		slog.String("note", "loopback only, disable PROFILING_ENABLED when done"))
	return &Server{server: server, addr: listener.Addr(), logger: logger}, nil
}

// requireLoopback rejects anything that could be reached from another host.
func requireLoopback(addr net.Addr) error {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() {
		return errors.New("profiling must bind a loopback address, for example 127.0.0.1:6060")
	}
	return nil
}

// routes lists the handlers explicitly instead of serving http.DefaultServeMux,
// so nothing else that registers on the default mux becomes reachable here.
func routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func serve(server *http.Server, listener net.Listener, logger *slog.Logger) {
	err := server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("profiling server stopped unexpectedly", slog.Any("error", err))
	}
}

// Shutdown stops the listener and restores the runtime sampling to its default,
// so a process that keeps running does not pay for profiling it no longer
// serves.
func (s *Server) Shutdown(ctx context.Context) error {
	runtime.SetMutexProfileFraction(0)
	runtime.SetBlockProfileRate(0)

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("stop profiling server: %w", err)
	}
	s.logger.Info("profiling server stopped")
	return nil
}
