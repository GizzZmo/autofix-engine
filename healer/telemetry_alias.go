package main

import "net/http"

// PrometheusHandler aliases PromHandler so startHTTPServer in main.go compiles.
func (t *Telemetry) PrometheusHandler() http.Handler {
	return t.PromHandler()
}
