package profiling

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestRequireLoopback(t *testing.T) {
	tests := []struct {
		name    string
		addr    net.Addr
		allowed bool
	}{
		{"ipv4 loopback", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6060}, true},
		{"ipv6 loopback", &net.TCPAddr{IP: net.IPv6loopback, Port: 6060}, true},
		{"all interfaces ipv4", &net.TCPAddr{IP: net.IPv4zero, Port: 6060}, false},
		{"all interfaces ipv6", &net.TCPAddr{IP: net.IPv6zero, Port: 6060}, false},
		{"lan address", &net.TCPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 6060}, false},
		{"non tcp", &net.UnixAddr{Name: "/tmp/prof.sock", Net: "unix"}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireLoopback(test.addr)
			if test.allowed && err != nil {
				t.Fatalf("loopback address rejected: %v", err)
			}
			if !test.allowed && err == nil {
				t.Fatal("non-loopback address accepted")
			}
		})
	}
}

func TestStartRejectsNonLoopback(t *testing.T) {
	server, err := Start("0.0.0.0:0", nil)
	if err == nil {
		_ = server.Shutdown(context.Background())
		t.Fatal("wildcard bind was accepted")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRoutesDoNotExposeApplicationPaths(t *testing.T) {
	handler := routes()
	for _, path := range []string{"/", "/api/v1/transfers", "/health", "/metrics"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, response.Code)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("pprof index returned %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "goroutine") {
		t.Fatal("pprof index did not list the goroutine profile")
	}
}

func TestStartAndShutdown(t *testing.T) {
	server, err := Start("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("start profiling: %v", err)
	}
	if address := server.Addr(); address == nil || !address.(*net.TCPAddr).IP.IsLoopback() {
		t.Fatalf("unexpected listener address: %v", address)
	}

	if got := runtime.SetMutexProfileFraction(-1); got != mutexProfileFraction {
		t.Fatalf("mutex fraction = %d, want %d", got, mutexProfileFraction)
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown profiling: %v", err)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Fatalf("mutex fraction after shutdown = %d, want 0", got)
	}
}

func TestProfileIsServedOnLoopbackOnly(t *testing.T) {
	server, err := Start("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("start profiling: %v", err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	// The listener is reached through its socket, not through the API mux.
	response, err := http.Get("http://" + server.Addr().String() + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatalf("fetch goroutine profile: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("goroutine profile returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if !strings.Contains(string(body), "goroutine profile") {
		t.Fatal("unexpected profile body")
	}
}
