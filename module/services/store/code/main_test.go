package main

import (
	"strings"
	"testing"
)

func TestMigrateStoreRequiresOwnerConnection(t *testing.T) {
	err := migrateStore("")
	if err == nil || !strings.Contains(err.Error(), "owner connection") {
		t.Fatalf("migrateStore() error = %v, want missing owner connection error", err)
	}
}
