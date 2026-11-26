// Command healthcheck probes an HTTP endpoint and exits non-zero when it does
// not answer with a 2xx status. The runtime image has no shell and no curl, so
// container health checks call this binary instead.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/ready", "endpoint to probe")
	timeout := flag.Duration("timeout", 3*time.Second, "probe timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	response, err := client.Get(*url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		fmt.Fprintf(os.Stderr, "healthcheck: %s returned %d\n", *url, response.StatusCode)
		os.Exit(1)
	}
}
