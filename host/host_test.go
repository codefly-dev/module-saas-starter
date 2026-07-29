package host

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

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
