package observability

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

// StatusRecorder remembers the status code a handler produced and forwards
// everything else to the wrapped writer. Metrics and tracing both need the
// final status, so they share one implementation instead of stacking wrappers.
type StatusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func NewStatusRecorder(writer http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: writer, status: http.StatusOK}
}

func (r *StatusRecorder) Status() int {
	return r.status
}

func (r *StatusRecorder) HeaderWritten() bool {
	return r.wroteHeader
}

// Fail records status as the outcome when the handler never wrote a header,
// which is what a panic looks like from the outside. It does not write.
func (r *StatusRecorder) Fail(status int) {
	if !r.wroteHeader {
		r.status = status
	}
}

func (r *StatusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *StatusRecorder) Write(body []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.ResponseWriter.Write(body)
}

func (r *StatusRecorder) Flush() {
	r.WriteHeader(http.StatusOK)
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *StatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *StatusRecorder) Push(target string, options *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
