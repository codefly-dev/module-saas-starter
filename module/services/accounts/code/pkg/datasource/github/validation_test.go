package github

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// Public is an affirmative answer only, proven by reading the repository with no
// credential at all — and it costs no REST call.
func TestRepositoryIsPublic(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.repo("docs")(map[string]*string{"docs/a.md": str("a")})

	public, err := gh.client("").RepositoryIsPublic(context.Background(), "acme/docs")
	if err != nil || !public {
		t.Fatalf("public repo = %v, %v; want public", public, err)
	}
	if _, err := gh.client("").RepositoryIsPublic(context.Background(), "acme/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repo err = %v, want ErrNotFound", err)
	}
	if public, err := gh.client("tok").RepositoryIsPublic(context.Background(), "acme/docs"); err == nil || public {
		t.Fatal("a client holding a credential proved a repository public")
	}
	if gh.restCalls.Load() != 0 {
		t.Fatalf("the visibility proof spent %d REST calls, want 0", gh.restCalls.Load())
	}
}

// A repository that needs a credential is not public: GitHub answers the
// anonymous read as it answers a missing repository.
func TestRepositoryNeedingACredentialIsNotPublic(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.requireAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))
	gh.repo("docs")(map[string]*string{"docs/a.md": str("a")})
	if public, err := gh.client("").RepositoryIsPublic(context.Background(), "acme/docs"); !errors.Is(err, ErrNotFound) || public {
		t.Fatalf("private repo = %v, %v; want ErrNotFound", public, err)
	}
}

// The unauthenticated limit is metered per IP rather than per credential, so it
// is reported distinctly — but still as a rate limit to a caller asking only
// that — and carries its reset when one is known.
func TestRateLimitErrorMatchesItsSentinels(t *testing.T) {
	reset := time.Unix(1_700_000_000, 0).UTC()
	anon := &RateLimitError{ResetAt: reset, Unauthenticated: true}
	if !errors.Is(anon, ErrRateLimited) || !errors.Is(anon, ErrUnauthenticatedRateLimited) {
		t.Fatal("an unauthenticated rate limit must match both sentinels")
	}
	authed := &RateLimitError{}
	if !errors.Is(authed, ErrRateLimited) || errors.Is(authed, ErrUnauthenticatedRateLimited) {
		t.Fatal("an authenticated rate limit must match only ErrRateLimited")
	}
	if got := anon.Error(); got != ErrUnauthenticatedRateLimited.Error()+" until 2023-11-14T22:13:20Z" {
		t.Fatalf("error = %q", got)
	}
}
