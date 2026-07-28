package main

import (
	"accounts/pkg/metrics"
	"fmt"
	"os"
)

func main() {
	if err := metrics.WriteDefaultSeedBundle(os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
