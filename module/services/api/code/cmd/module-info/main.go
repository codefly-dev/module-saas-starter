// module-info is a CLI that aggregates per-service introspection
// across every service in a codefly module and prints a merged
// view as JSON.
//
// Usage:
//
//	module-info \
//	    --service api=http://localhost:5962/v1/.well-known/service-info \
//	    [--service auth-sidecar=http://localhost:7000/v1/.well-known/service-info]
//
// Today saas-starter has only the api service exposing
// IntrospectionService — the loop below works on a single endpoint.
// As other services gain their own IntrospectionService, add
// --service entries.
//
// Future: read module.codefly.yaml directly and resolve endpoints
// from codefly's network manager. For now the CLI takes explicit
// URLs to keep dependencies minimal.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"api/pkg/moduleinfo"
)

type endpointFlag []moduleinfo.Endpoint

func (e *endpointFlag) String() string { return "" }
func (e *endpointFlag) Set(v string) error {
	idx := strings.Index(v, "=")
	if idx <= 0 {
		return fmt.Errorf("expected name=url, got %q", v)
	}
	*e = append(*e, moduleinfo.Endpoint{Name: v[:idx], URL: v[idx+1:]})
	return nil
}

func main() {
	var endpoints endpointFlag
	flag.Var(&endpoints, "service", "name=url; repeatable. URL points at /v1/.well-known/service-info")
	timeout := flag.Duration("timeout", 5*time.Second, "per-endpoint HTTP timeout")
	flag.Parse()

	if len(endpoints) == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one --service flag is required")
		flag.Usage()
		os.Exit(2)
	}

	view, errs := moduleinfo.Aggregate(context.Background(), endpoints, *timeout)
	for name, err := range errs {
		fmt.Fprintf(os.Stderr, "warn: service %s failed: %v\n", name, err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(view); err != nil {
		fmt.Fprintln(os.Stderr, "error: encoding output:", err)
		os.Exit(1)
	}
	if len(errs) > 0 {
		// Exit 1 so CI / scripts can detect partial failure even
		// though we still printed what we got.
		os.Exit(1)
	}
}
