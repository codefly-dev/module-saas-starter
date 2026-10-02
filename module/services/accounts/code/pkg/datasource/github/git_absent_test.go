package github

import (
	"context"
	"errors"
	"testing"
)

// The fetch runs git, so an image without it cannot read a repository. That is
// what happened on a cell: the refusal below fired on every repository sync,
// correctly and without retrying, because the service image carried no git.
//
// The declaration that fixes it lives in the service manifest
// (spec.runtime-packages), and module/tools holds the manifest to this package.
// These tests hold the other half: the refusal itself must stay exactly as it
// is. It is the only thing standing between "no git" and a sync that fails
// somewhere deeper, later, and less legibly — a mirror half-built, a git
// invocation reported as a network error, or a retry loop against a condition
// no retry can change. A future change that makes a missing git surface as
// anything other than ErrGitUnavailable from the first call reintroduces that,
// so each entry point is asserted separately rather than through one path.
//
// PATH is emptied rather than git renamed: exec.LookPath consults PATH, so this
// reproduces the image's condition (a real git exists on the machine, and is not
// reachable from this process) without touching anything outside the test.
func TestEveryGitEntryPointRefusesWhenGitIsAbsentFromPath(t *testing.T) {
	// t.Setenv forbids t.Parallel; PATH is process-wide.
	t.Setenv("PATH", t.TempDir())

	c := New("", "", WithCacheRoot(t.TempDir()))
	ctx := context.Background()

	t.Run("OpenRepository", func(t *testing.T) {
		if _, err := c.OpenRepository(ctx, ws, "acme/docs"); !errors.Is(err, ErrGitUnavailable) {
			t.Fatalf("OpenRepository with no git on PATH = %v, want ErrGitUnavailable", err)
		}
	})
	t.Run("DefaultBranch", func(t *testing.T) {
		if _, err := c.DefaultBranch(ctx, "acme/docs"); !errors.Is(err, ErrGitUnavailable) {
			t.Fatalf("DefaultBranch with no git on PATH = %v, want ErrGitUnavailable", err)
		}
	})
	t.Run("ResolveCommit", func(t *testing.T) {
		if _, err := c.ResolveCommit(ctx, "acme/docs", "main"); !errors.Is(err, ErrGitUnavailable) {
			t.Fatalf("ResolveCommit with no git on PATH = %v, want ErrGitUnavailable", err)
		}
	})
	t.Run("RepositoryIsPublic", func(t *testing.T) {
		if _, err := c.RepositoryIsPublic(ctx, "acme/docs"); !errors.Is(err, ErrGitUnavailable) {
			t.Fatalf("RepositoryIsPublic with no git on PATH = %v, want ErrGitUnavailable", err)
		}
	})
}

// The refusal must not be spent on a request first. A sync that reached the
// remote and then discovered it had no git would burn a request, and for a
// public read that request is metered per source IP — so the refusal is worth
// nothing if it arrives after the cost it exists to avoid.
func TestGitAbsenceIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	c := New("", "", WithCacheRoot(t.TempDir()))
	if _, err := c.DefaultBranch(context.Background(), "acme/docs"); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("DefaultBranch = %v, want ErrGitUnavailable", err)
	}
	if got := c.Requests(); got != 0 {
		t.Fatalf("refusing for a missing git spent %d requests, want 0", got)
	}
}
