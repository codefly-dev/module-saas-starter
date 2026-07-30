// Package host implements policy.PermissionsBackend and
// policy.EscalationGrantor backed by a saas-starter API
// instance. Hosts (codefly CLI, Mind) use this to wire the
// abstract policy interfaces in core/policy to their concrete
// saas-starter deployment.
//
// **Why HTTP/JSON instead of gRPC.** Plain HTTP keeps this
// package free of the saas-starter proto codegen (which lives
// behind a local-only "api" import path). The request/response
// shapes are stable — they're tied to the proto definitions
// via grpc-gateway annotations — but redefined here as Go
// structs so the host can build without depending on the API
// service's internal module.
//
// **Why this lives in the saas-starter agent module.** Core has
// no dependency on saas-starter (one-way arrow), and the CLI
// shouldn't redefine saas-starter's wire types either. Putting
// the adapter in saas-starter's own go module keeps the wire-
// type ownership next to the proto and gives the CLI a stable
// import path: github.com/codefly-dev/agents/modules/saas-starter/host.
//
// **Authentication.** Calls to privileged saas-starter RPCs
// (Decide, RequestDelegation, DecideDelegation) require either
// a JWT bearer or the X-Codefly-Internal-Token shared-secret
// header. Configure via Auth in the constructor; the adapter
// passes whichever is set on every request.
//
// **Streaming WaitForDelegation.** grpc-gateway exposes the
// server-streaming RPC as a single GET that returns a
// chunked-transfer body of newline-delimited JSON envelopes
// (`{"result": {...}}` per line). The adapter reads one
// terminal envelope and disconnects.
package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/codefly-dev/core/policy"
)

// Auth bundles the credentials the host uses when calling
// saas-starter. Exactly one of BearerToken / InternalToken
// SHOULD be set; if both are, BearerToken wins (the JWT
// carries the strongest identity claim).
type Auth struct {
	// BearerToken is the host's service-account JWT, set when
	// codefly authenticates as a user/service to saas-starter.
	BearerToken string

	// InternalToken is the shared-secret matching saas-starter's
	// CODEFLY_INTERNAL_TOKEN env var. Used for privileged-internal
	// RPCs the host invokes on behalf of the platform itself
	// (no user identity to delegate from). MUST stay out of logs.
	InternalToken string
}

// Backend is the saas-starter-backed implementation of
// policy.PermissionsBackend. Build via NewBackend; configure via
// the With* helpers.
//
// **Thread-safety.** Backend is safe for concurrent use as long
// as the underlying http.Client is (the stdlib default Client
// is). No mutable state inside.
type Backend struct {
	baseURL string
	client  *http.Client
	auth    Auth

	// Timeout applies to each Decide call. Zero leaves it to the
	// caller's ctx. Recommended: 1-5 seconds for hot-path
	// permission checks (gateway evaluator).
	Timeout time.Duration
}

// NewBackend constructs a Backend. baseURL is the saas-starter
// API base (e.g. "http://localhost:21931"). auth supplies the
// credentials the host calls with.
func NewBackend(baseURL string, auth Auth) *Backend {
	return &Backend{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		auth:    auth,
	}
}

// WithHTTPClient swaps the underlying http.Client. Use to
// inject custom transport (TLS, retries, tracing). Returns the
// receiver for fluent configuration.
func (b *Backend) WithHTTPClient(c *http.Client) *Backend {
	if c != nil {
		b.client = c
	}
	return b
}

// WithTimeout caps the per-call timeout. Zero leaves it to ctx.
func (b *Backend) WithTimeout(d time.Duration) *Backend {
	b.Timeout = d
	return b
}

// =====================================================================
// PermissionsBackend
// =====================================================================
//
// Decide calls POST /v1/permissions:decide. The saas-starter
// adapter answers with (allowed, reason, decision_path). On
// transport / 5xx error we surface the error so SaasPDP's
// fail-closed path engages — the policy layer's invariant is
// that an unreachable backend denies, never silently allows.

type decideRequest struct {
	PrincipalID string `json:"principal_id"`
	Action      string `json:"action"`
	Resource    string `json:"resource"`
	OrgID       string `json:"org_id"`
}

type decideResponse struct {
	Decision     string `json:"decision"` // "DECISION_ALLOW" / "DECISION_DENY"
	Reason       string `json:"reason"`
	DecisionPath string `json:"decision_path"`
}

// Decide implements policy.PermissionsBackend.
func (b *Backend) Decide(ctx context.Context, principalID, action, resource, orgID, scope string) (allowed bool, reason, decisionPath string, err error) {
	body, _ := json.Marshal(decideRequest{
		PrincipalID: principalID,
		Action:      action,
		Resource:    resource,
		OrgID:       orgID,
	})

	if b.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/permissions:decide", bytes.NewReader(body))
	if err != nil {
		return false, "", "", fmt.Errorf("saas-host: build decide request: %w", err)
	}
	b.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return false, "", "", fmt.Errorf("saas-host: decide round-trip: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("saas-host: close decide response: %w", closeErr))
		}
	}()

	if resp.StatusCode/100 != 2 {
		return false, "", "", fmt.Errorf("saas-host: decide HTTP %d", resp.StatusCode)
	}

	var dec decideResponse
	if err := json.NewDecoder(resp.Body).Decode(&dec); err != nil {
		return false, "", "", fmt.Errorf("saas-host: decode decide response: %w", err)
	}

	allowed = dec.Decision == "DECISION_ALLOW"
	return allowed, dec.Reason, dec.DecisionPath, nil
}

// =====================================================================
// EscalationGrantor
// =====================================================================
//
// Request orchestrates the M7 flow:
//   1. POST /v1/delegations → grant id (status: pending or
//      already-approved via M8 pattern match)
//   2. GET  /v1/delegations/{id}:wait → block until terminal
//   3. Translate the terminal event into a policy.EscalationResult
//      (verifies the scoped-auth token via the global
//      TokenVerifier so callers get a ready-to-use Authorization).

// Grantor is the saas-starter-backed EscalationGrantor.
//
// Reuses Backend's transport + auth (the same saas-starter
// instance handles both surfaces). Construct via NewGrantor.
type Grantor struct {
	backend *Backend

	// Verifier verifies the encoded scoped-auth token returned
	// on approve. When nil, the grantor returns the encoded
	// Token without a populated Authorization (callers can still
	// attach the header; the receiving plugin verifies on its
	// side).
	Verifier *policy.TokenVerifier
}

// NewGrantor constructs a Grantor that shares the supplied
// Backend's HTTP client and auth.
func NewGrantor(b *Backend) *Grantor {
	if b == nil {
		panic("saas-host: NewGrantor: backend must be non-nil")
	}
	return &Grantor{backend: b}
}

// WithVerifier installs the TokenVerifier used to validate
// approval-minted tokens. Recommended for production so the
// host catches mint failures early; safe to leave nil in
// dev/tests.
func (g *Grantor) WithVerifier(v *policy.TokenVerifier) *Grantor {
	g.Verifier = v
	return g
}

type requestDelegationBody struct {
	OrgID            string         `json:"org_id"`
	ActorPrincipalID string         `json:"actor_principal_id"`
	Action           string         `json:"action"`
	Resource         string         `json:"resource"`
	Justification    string         `json:"justification"`
	Context          map[string]any `json:"context,omitempty"`
	RiskLevel        string         `json:"risk_level,omitempty"`
	TimeoutSeconds   int32          `json:"timeout_seconds,omitempty"`
	Grantor          string         `json:"grantor,omitempty"`
}

type requestDelegationResp struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

type delegationEventEnvelope struct {
	Result *delegationEvent `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type delegationEvent struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	DecidedAt          string `json:"decided_at,omitempty"`
	GrantorPrincipalID string `json:"grantor_principal_id,omitempty"`
	Reason             string `json:"reason,omitempty"`
	ScopedAuthToken    string `json:"scoped_auth_token,omitempty"`
	MintedTokenID      string `json:"minted_token_id,omitempty"`
}

// Request implements policy.EscalationGrantor. Files the request
// then blocks on the streaming wait endpoint until the grant
// reaches a terminal state.
func (g *Grantor) Request(ctx context.Context, req policy.EscalationRequest) (*policy.EscalationResult, error) {
	if req.Principal == nil {
		return nil, errors.New("saas-host: nil principal")
	}

	// Default timeout 5min mirrors the proto-side default; saves
	// callers from forgetting to set one and waiting forever.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	body, _ := json.Marshal(requestDelegationBody{
		OrgID:            req.Principal.OrgID,
		ActorPrincipalID: req.Principal.ID,
		Action:           req.Action,
		Resource:         req.Resource,
		Justification:    req.Justification,
		Context:          req.Context,
		TimeoutSeconds:   int32(timeout / time.Second),
		Grantor:          req.Grantor,
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.backend.baseURL+"/v1/delegations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("saas-host: build delegation request: %w", err)
	}
	g.backend.applyAuth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.backend.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("saas-host: file delegation: %w", err)
	}
	body0, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		readErr = fmt.Errorf("saas-host: read delegation response: %w", readErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("saas-host: close delegation response: %w", closeErr)
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("saas-host: file delegation HTTP %d: %s", resp.StatusCode, string(body0))
	}

	var filed requestDelegationResp
	if err := json.Unmarshal(body0, &filed); err != nil {
		return nil, fmt.Errorf("saas-host: decode delegation response: %w", err)
	}

	// Wait for terminal. Re-uses ctx so caller cancellation
	// (Timeout / parent cancel) propagates into the streaming
	// HTTP read.
	event, err := g.waitForTerminal(ctx, filed.ID, req.Principal.OrgID)
	if err != nil {
		return nil, err
	}

	return g.eventToResult(event, filed.ID), nil
}

// waitForTerminal opens the streaming :wait endpoint and returns
// the first decoded event. Each line of the response body is one
// `{"result": <event>}` envelope (grpc-gateway's standard wrapper
// for server-streaming JSON); we read until we get one with a
// terminal status.
func (g *Grantor) waitForTerminal(ctx context.Context, grantID, orgID string) (event *delegationEvent, err error) {
	q := url.Values{}
	q.Set("org_id", orgID)
	waitURL := fmt.Sprintf("%s/v1/delegations/%s:wait?%s",
		g.backend.baseURL,
		url.PathEscape(grantID),
		q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, waitURL, nil)
	if err != nil {
		return nil, fmt.Errorf("saas-host: build wait request: %w", err)
	}
	g.backend.applyAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := g.backend.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("saas-host: wait round-trip: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("saas-host: close wait response: %w", closeErr))
		}
	}()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("saas-host: wait HTTP %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env delegationEventEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			// Tolerate stray lines (some gateways emit a
			// trailing comma or padding). Skip and continue.
			continue
		}
		if env.Error != nil {
			return nil, fmt.Errorf("saas-host: wait stream error: %s", env.Error.Message)
		}
		if env.Result == nil {
			continue
		}
		switch env.Result.Status {
		case "approved", "denied", "expired", "cancelled":
			return env.Result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("saas-host: wait stream read: %w", err)
	}
	return nil, errors.New("saas-host: wait stream closed without terminal event")
}

// eventToResult translates the wire event into the policy result
// shape. On approve, verifies the encoded token (when a Verifier
// is configured) so callers receive a ready-to-attach
// ScopedAuthorization rather than just an opaque blob.
func (g *Grantor) eventToResult(ev *delegationEvent, grantID string) *policy.EscalationResult {
	r := &policy.EscalationResult{
		Reason:  ev.Reason,
		Decider: ev.GrantorPrincipalID,
		GrantID: grantID,
		Token:   ev.ScopedAuthToken,
	}
	switch ev.Status {
	case "approved":
		r.Decision = policy.EscalationApproved
		if g.Verifier != nil && ev.ScopedAuthToken != "" {
			// Empty Expectations means "verify signature + expiry
			// only; defer claim/audience checks to the receiving
			// plugin." Host can't know the audience here — it's
			// whichever plugin the SDK calls next.
			if sa, err := g.Verifier.Verify(ev.ScopedAuthToken, policy.VerifyExpectations{}); err == nil {
				r.Authorization = sa
			}
			// Verifier failure is intentionally swallowed: the
			// caller still has the encoded Token and the
			// receiving plugin re-verifies. Logging it would
			// require a logger in this package — kept zero-dep.
		}
	case "denied":
		r.Decision = policy.EscalationDenied
	case "expired", "cancelled":
		r.Decision = policy.EscalationTimedOut
	}
	return r
}

// =====================================================================
// Helpers
// =====================================================================

// applyAuth writes the configured credential header onto req.
// JWT (Bearer) wins over the internal-token shared secret.
func (b *Backend) applyAuth(req *http.Request) {
	switch {
	case b.auth.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+b.auth.BearerToken)
	case b.auth.InternalToken != "":
		req.Header.Set("X-Codefly-Internal-Token", b.auth.InternalToken)
	}
}

// --- Compile-time assertions --------------------------------------

var _ policy.PermissionsBackend = (*Backend)(nil)
var _ policy.EscalationGrantor = (*Grantor)(nil)
