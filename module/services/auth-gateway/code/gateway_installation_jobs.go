package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/google/uuid"
)

const installationCallTimeout = 20 * time.Second

type installationStepClaim struct {
	JobID        string          `json:"job_id"`
	Worker       string          `json:"worker"`
	LeaseToken   string          `json:"lease_token"`
	Attempt      uint32          `json:"attempt"`
	RouteAlias   string          `json:"route_alias"`
	TargetID     string          `json:"target_id"`
	BindingID    string          `json:"binding_id"`
	WorkContext  string          `json:"work_context"`
	Installation json.RawMessage `json:"installation"`
}

// Accounts retains the queue and signer. This consumer is here because the
// gateway already owns the verified registry resolution and workload transport.
// No user route can submit an upstream, credential, installation or job to it.
type installationJobConsumer struct {
	accountsURL, internalToken, worker string
	client                             *http.Client
	deliver                            func(context.Context, *installationStepClaim) (int, string)
}

func (g *Gateway) runInstallationJobs(ctx context.Context, accountsURL string) {
	c := &installationJobConsumer{accountsURL: accountsURL, internalToken: g.authz.internalToken,
		worker: "gateway-install-" + uuid.NewString(), client: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, deliver: g.deliverInstallationStep}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := c.runOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("installation consumer: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *installationJobConsumer) post(ctx context.Context, operation string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.accountsURL+"/internal/installation-steps/"+operation, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Codefly-Internal-Token", c.internalToken)
	return c.client.Do(req)
}

func (c *installationJobConsumer) runOnce(ctx context.Context) error {
	resp, err := c.post(ctx, "claim", map[string]string{"worker": c.worker})
	if err != nil {
		return errors.New("accounts claim unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("accounts claim returned %d", resp.StatusCode)
	}
	var claim installationStepClaim
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&claim); err != nil {
		return errors.New("invalid accounts claim")
	}
	if claim.Worker != c.worker || claim.JobID == "" || claim.LeaseToken == "" || claim.WorkContext == "" || claim.TargetID == "" || claim.BindingID == "" {
		return errors.New("incomplete accounts claim")
	}
	callCtx, cancel := context.WithTimeout(ctx, installationCallTimeout)
	status, reason := c.deliver(callCtx, &claim)
	cancel()
	outcome, err := c.post(ctx, "finish", map[string]any{"job_id": claim.JobID, "worker": claim.Worker, "lease_token": claim.LeaseToken, "attempt": claim.Attempt, "status": status, "reason": reason})
	if err != nil {
		return errors.New("accounts outcome unavailable; lease expiry will recover")
	}
	defer outcome.Body.Close()
	if outcome.StatusCode != http.StatusNoContent {
		return fmt.Errorf("accounts outcome returned %d", outcome.StatusCode)
	}
	return nil
}

func (g *Gateway) deliverInstallationStep(ctx context.Context, claim *installationStepClaim) (int, string) {
	entry, resolution := g.solutions.resolveRoutingOfKind(ctx, claim.RouteAlias, solutionSurface)
	if resolution != solutionRoutable || entry.TargetID != claim.TargetID || entry.BindingID != claim.BindingID {
		return http.StatusServiceUnavailable, ""
	}
	transport, err := g.workload.upstreamTransport(entry.WorkloadSPIFFEID)
	if err != nil {
		return http.StatusServiceUnavailable, ""
	}
	defer transport.CloseIdleConnections()
	return sendInstallationStep(ctx, &http.Client{Transport: transport, Timeout: installationCallTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, entry.Upstream.String()+"/_codefly/installations", claim)
}

func sendInstallationStep(ctx context.Context, client *http.Client, destination string, claim *installationStepClaim) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(claim.Installation))
	if err != nil {
		return http.StatusServiceUnavailable, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workcontext.WorkContextHeaderName, claim.WorkContext)
	resp, err := client.Do(req)
	if err != nil {
		return http.StatusServiceUnavailable, ""
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return http.StatusNoContent, ""
	case http.StatusConflict:
		// Only the explicit public field crosses into job history. Accounts
		// bounds it again in UTF-8 bytes before persisting the refusal.
		var refusal struct {
			Reason string `json:"reason"`
		}
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 4097))
		if err := decoder.Decode(&refusal); err != nil || decoder.Decode(new(any)) != io.EOF {
			return http.StatusConflict, ""
		}
		return http.StatusConflict, refusal.Reason
	default:
		return http.StatusServiceUnavailable, ""
	}
}
