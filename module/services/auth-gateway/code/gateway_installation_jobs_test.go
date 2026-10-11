package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

type installationRoundTrip func(*http.Request) (*http.Response, error)

func (f installationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// HOST-INST-003..005: the worker carries the installation credential and the
// exact revision/scope, acknowledges only 204, and retries every transport fault.
func TestInstallationDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   int
		reason string
	}{
		{"acknowledged", 204, "", nil, 204, ""},
		{"refused", 409, `{"reason":"Example refusal"}`, nil, 409, "Example refusal"},
		{"malformed refusal", 409, `private diagnostic`, nil, 409, ""},
		{"unavailable", 503, `private diagnostic`, nil, 503, ""},
		{"unexpected success", 200, "", nil, 503, ""},
		{"redirect", 302, "", nil, 503, ""},
		{"transport", 0, "", errors.New("private transport diagnostic"), 503, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := &installationStepClaim{WorkContext: "installation-credential", Installation: json.RawMessage(`{"installation_id":"example-id","organization_id":"example-org","revision":7,"scope":[]}`)}
			client := &http.Client{Transport: installationRoundTrip(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "/_codefly/installations", r.URL.Path)
				require.Equal(t, claim.WorkContext, r.Header.Get(workcontext.WorkContextHeaderName))
				require.Empty(t, r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("X-Codefly-Internal-Token"))
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.JSONEq(t, string(claim.Installation), string(raw))
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			status, reason := sendInstallationStep(context.Background(), client, "https://solution.example/_codefly/installations", claim)
			require.Equal(t, tc.want, status)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestInstallationConsumerReportsLeaseAndOutcome(t *testing.T) {
	var operations []string
	c := &installationJobConsumer{accountsURL: "http://accounts.example", internalToken: "private-token", worker: "example-worker"}
	c.client = &http.Client{Transport: installationRoundTrip(func(r *http.Request) (*http.Response, error) {
		operations = append(operations, r.URL.Path)
		require.Equal(t, "private-token", r.Header.Get("X-Codefly-Internal-Token"))
		if strings.HasSuffix(r.URL.Path, "/claim") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"job_id":"job","worker":"example-worker","lease_token":"fence","attempt":3,"target_id":"target","binding_id":"binding","work_context":"credential","installation":{"revision":7}}`))}, nil
		}
		var result map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&result))
		require.Equal(t, map[string]any{"job_id": "job", "worker": "example-worker", "lease_token": "fence", "attempt": float64(3), "status": float64(409), "reason": "Example refusal"}, result)
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	c.deliver = func(ctx context.Context, claim *installationStepClaim) (int, string) {
		_, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, "credential", claim.WorkContext)
		return 409, "Example refusal"
	}
	require.NoError(t, c.runOnce(context.Background()))
	require.Equal(t, []string{"/internal/installation-steps/claim", "/internal/installation-steps/finish"}, operations)
}
