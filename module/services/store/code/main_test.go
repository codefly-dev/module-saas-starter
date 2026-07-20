package main

import (
	"strings"
	"testing"
)

func TestRunRequiresDatabaseURL(t *testing.T) {
	t.Setenv(databaseURLEnv, "")

	err := run()
	if err == nil || !strings.Contains(err.Error(), databaseURLEnv) {
		t.Fatalf("run() error = %v, want missing %s error", err, databaseURLEnv)
	}
}
