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
	"strconv"
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
// gateway already owns the verified registry resolution and the guarded
// transport to a declared upstream. No user route can submit an upstream,
// credential, installation or job to it.
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
		// Named, because a step that cannot be delivered is retried and then
		// dead-lettered with a generic reason, and the operator reading the
		// installation needs to know which of these it was.
		log.Printf("installation step: route %q is not deliverable: resolution=%v target=%q/%q binding=%q/%q",
			claim.RouteAlias, resolution, entry.TargetID, claim.TargetID, entry.BindingID, claim.BindingID)
		return http.StatusServiceUnavailable, ""
	}
	// The step goes where the proxy goes: the address the Codefly SDK resolved
	// for the solution's declared endpoint, over the same guarded plain-HTTP
	// transport, with no second route to a workload.
	return sendInstallationStep(ctx, &http.Client{Transport: g.registeredTransport, Timeout: installationCallTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, entry.Upstream.String()+"/_codefly/installations", claim)
}

func sendInstallationStep(ctx context.Context, client *http.Client, destination string, claim *installationStepClaim) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(claim.Installation))
	if err != nil {
		return http.StatusServiceUnavailable, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workcontext.HeaderName, claim.WorkContext)
	// The capability is sealed to one installation and revision; the carrier
	// names them beside it, and the runtime refuses a step whose carrier and
	// seal disagree (sdk-go/workcontext checkCarriedInstallation). They are
	// read off the step itself, which accounts wrote from the same record.
	var sealed struct {
		InstallationID string `json:"installation_id"`
		Revision       uint64 `json:"revision"`
	}
	if err := json.Unmarshal(claim.Installation, &sealed); err != nil || sealed.InstallationID == "" {
		log.Printf("installation step: %s: the step names no installation: %v", destination, err)
		return http.StatusServiceUnavailable, ""
	}
	req.Header.Set(workcontext.InstallationIDHeaderName, sealed.InstallationID)
	req.Header.Set(workcontext.InstallationRevisionHeaderName, strconv.FormatUint(sealed.Revision, 10))
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("installation step: %s: %v", destination, err)
		return http.StatusServiceUnavailable, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusConflict {
		log.Printf("installation step: %s answered %d", destination, resp.StatusCode)
	}
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
