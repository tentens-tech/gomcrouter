// Command gomcrouter is a high performance memcached request router.
//
// It accepts memcached text protocol traffic, routes every operation by
// configurable policies (default, all-fastest, miss-failover, local and
// per-operation routes) and forwards the requests to upstream memcached
// nodes through an event-driven upstream engine. Health checks, DNS
// re-resolution and Prometheus metrics are built in.
//
// Usage:
//
//	gomcrouter -f router.yaml [-l tcp://:8080] [-L :9090]
//
// See the README for the configuration and all flags:
// https://github.com/tentens-tech/gomcrouter#readme
package main

import (
	"github.com/tentens-tech/gomcrouter/cmd"
)

func main() {
	cmd.Execute()
}
