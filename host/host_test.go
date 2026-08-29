package host

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/policy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorReadCloser struct {
	io.Reader
	readErr  error
	closeErr error
}

func (r *errorReadCloser) Read(p []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.Reader.Read(p)
}

func (r *errorReadCloser) Close() error {
	return r.closeErr
}

func TestDecideReturnsResponseCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	backend := NewBackend("http://saas.test", Auth{}).WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: &errorReadCloser{
					Reader:   strings.NewReader(`{"decision":"DECISION_ALLOW"}`),
					closeErr: closeErr,
				},
			}, nil
		}),
	})

	_, _, _, err := backend.Decide(context.Background(), "user", "read", "resource", "org", "")
	if !errors.Is(err, closeErr) {
		t.Fatalf("Decide() error = %v, want response close error", err)
	}
}

func TestRequestReturnsDelegationResponseReadAndCloseErrors(t *testing.T) {
	readErr := errors.New("read failed")
	closeErr := errors.New("close failed")
	backend := NewBackend("http://saas.test", Auth{}).WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: &errorReadCloser{
					Reader:   strings.NewReader(""),
					readErr:  readErr,
					closeErr: closeErr,
				},
			}, nil
		}),
	})

	_, err := NewGrantor(backend).Request(context.Background(), policy.EscalationRequest{
		Principal: &policy.Principal{ID: "user", OrgID: "org"},
	})
	if !errors.Is(err, readErr) {
		t.Errorf("Request() error = %v, want response read error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Errorf("Request() error = %v, want response close error", err)
	}
}

func TestRequestReturnsWaitResponseCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	backend := NewBackend("http://saas.test", Auth{}).WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := io.ReadCloser(io.NopCloser(strings.NewReader(`{"id":"grant"}`)))
			if req.Method == http.MethodGet {
				body = &errorReadCloser{
					Reader:   strings.NewReader("{\"result\":{\"id\":\"grant\",\"status\":\"approved\"}}\n"),
					closeErr: closeErr,
				}
			}
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		}),
	})

	_, err := NewGrantor(backend).Request(context.Background(), policy.EscalationRequest{
		Principal: &policy.Principal{ID: "user", OrgID: "org"},
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("Request() error = %v, want wait response close error", err)
	}
}

// TestEventToResultVerifierPath guards the approve/verify branch of
// eventToResult. That branch reads Grantor.Verifier, and the only way to
// install a verifier is to assign the exported field directly (there is no
// fluent setter) — so this test also proves the verifier stays wire-able and
// the approve path still consults it after the WithVerifier setter was removed.
func TestEventToResultVerifierPath(t *testing.T) {
	secret := []byte("test-hmac-secret-at-least-32-bytes-long")
	token, minted, err := policy.Mint(policy.MintInput{
		Principal: &policy.Principal{ID: "user-1", Kind: "human", OrgID: "org-1"},
		Action:    "read",
		TTL:       time.Minute,
	}, secret)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	ev := &delegationEvent{
		Status:             "approved",
		ScopedAuthToken:    token,
		GrantorPrincipalID: "admin-1",
		Reason:             "ok",
	}

	t.Run("verifier set populates Authorization", func(t *testing.T) {
		g := NewGrantor(NewBackend("http://saas.test", Auth{}))
		// The sole wiring path after WithVerifier's removal: assign the
		// exported field. If this stops compiling or the branch stops
		// reading it, production loses ready-to-attach authorizations.
		g.Verifier = policy.NewTokenVerifier().WithHMACSecret(secret)

		r := g.eventToResult(ev, "grant-1")
		if r.Decision != policy.EscalationApproved {
			t.Fatalf("Decision = %v, want approved", r.Decision)
		}
		if r.Token != token {
			t.Fatalf("Token = %q, want minted token", r.Token)
		}
		if r.Authorization == nil {
			t.Fatal("Authorization is nil; verifier branch did not populate it")
		}
		if r.Authorization.PrincipalID != minted.PrincipalID {
			t.Fatalf("Authorization.PrincipalID = %q, want %q", r.Authorization.PrincipalID, minted.PrincipalID)
		}
	})

	t.Run("nil verifier leaves Authorization unset", func(t *testing.T) {
		g := NewGrantor(NewBackend("http://saas.test", Auth{}))

		r := g.eventToResult(ev, "grant-1")
		if r.Decision != policy.EscalationApproved {
			t.Fatalf("Decision = %v, want approved", r.Decision)
		}
		if r.Token != token {
			t.Fatalf("Token = %q, want minted token", r.Token)
		}
		if r.Authorization != nil {
			t.Fatal("Authorization populated with nil Verifier; want nil per documented behavior")
		}
	})
}
