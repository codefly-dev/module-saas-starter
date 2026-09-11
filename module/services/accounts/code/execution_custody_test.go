package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionCustodyMountDisabledAndRevocationRequired(t *testing.T) {
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", "")
	server, err := configuredExecutionCustody(nil, nil, nil, "", nil, false, true)
	if err != nil || server != nil {
		t.Fatal("absent projection must leave listener disabled")
	}
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", "/not-read-before-revocation-check")
	for _, mode := range [][2]bool{{false, false}, {true, true}} {
		if _, err := configuredExecutionCustody(nil, nil, nil, "", nil, mode[0], mode[1]); err == nil || err.Error() != "execution custody requires fail-closed access-token revocation" {
			t.Fatal("unsafe revocation configuration accepted")
		}
	}
}

func TestCustodyProjectionSupportsPrivateAtomicMount(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "version")
	link := filepath.Join(dir, "config")
	if err := os.WriteFile(target, []byte("{}"), 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := projectedCustodyFile(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := projectedCustodyFile(link); err == nil {
		t.Fatal("world-readable projection accepted")
	}
}

func TestCustodyRequiresExplicitDatabaseTransport(t *testing.T) {
	t.Setenv("EXECUTION_CUSTODY_CONFIG_FILE", "/not-read-before-transport-check")
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "")
	if _, err := configuredExecutionCustody(nil, nil, nil, "", nil, true, false); err == nil || err.Error() != "execution custody requires explicit verified-tls or local-identity-proxy database transport" {
		t.Fatal("unspecified hosted database transport accepted")
	}
}
