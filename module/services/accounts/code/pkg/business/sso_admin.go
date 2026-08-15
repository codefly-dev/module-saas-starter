package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OrgSSOConfig — resolved view of an org's SSO state. Empty
// ConnectionID means SSO is not yet configured (admin hasn't run the
// portal flow, or admin Disabled it and we cleared the row).
//
// Status reflects the WorkOS Connections lifecycle as we observe it:
//
//	""             — no record / never configured
//	"linked"       — admin portal link minted, IdP setup pending
//	"active"       — connection live, users in domain SSO-routed
//	"disabled"     — admin paused; row preserved so re-enable is fast
type OrgSSOConfig struct {
	OrgID          string
	Provider       string
	ConnectionID   string
	OrganizationID string // WorkOS-side Organization id
	Status         string
	ConfiguredAt   *time.Time
}

// GetOrgSSO returns the org's SSO state. Returns (nil, nil) when no
// row has been written yet — the FE branches on existence. The
// underlying SSO columns live on `organizations` (RLS-protected,
// Phase 2F), so the read goes through WithOrgTx.
func (s *Service) GetOrgSSO(ctx context.Context, orgID string) (*OrgSSOConfig, error) {
	var cfg *OrgSSOConfig
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		c, err := s.store.GetOrgSSO(ctx, orgID)
		cfg = c
		return err
	}); err != nil {
		return nil, err
	}
	return cfg, nil
}

// StartSSOSetup either reuses an existing WorkOS Organization for
// this saas-starter org or creates one, then mints a one-shot
// admin-portal link the FE redirects the browser to. Returns the
// portal URL.
//
// actorID — the authenticated platform/org admin who initiated the
// flow; recorded as the audit-event actor (NOT the org id).
//
// Stub mode: when the optional Codefly identity management credential is not
// wired we don't make HTTP calls
// to WorkOS — instead we return a placeholder URL that points back
// at /admin/sso?demo=1. Lets local dev exercise the FE without
// needing real WorkOS credentials.
func (s *Service) StartSSOSetup(ctx context.Context, actorID, orgID, returnURL string) (string, error) {
	apiKey := s.ssoManagementAPIKey
	if apiKey == "" {
		// Dev / unit-test mode. Mark the row as "linked" so the FE
		// transitions to the post-setup view (and an operator can
		// click Disable to test that path).
		now := time.Now()
		if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			return s.store.UpsertOrgSSO(ctx, &OrgSSOConfig{
				OrgID:        orgID,
				Provider:     "workos",
				Status:       "linked",
				ConfiguredAt: &now,
			})
		}); err != nil {
			return "", fmt.Errorf("persist stub SSO setup: %w", err)
		}
		s.emit(ctx, actorID, "user", "sso.setup.started", "organization", orgID, orgID)
		return returnURL + "?demo=1", nil
	}

	client := &workosClient{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 10 * time.Second},
	}

	// Read existing SSO state under the org's tx (RLS-protected via
	// organizations).
	var cfg *OrgSSOConfig
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		c, err := s.store.GetOrgSSO(ctx, orgID)
		cfg = c
		return err
	}); err != nil {
		return "", fmt.Errorf("load existing sso row: %w", err)
	}

	// Re-use the WorkOS org id if we created one previously. Otherwise
	// create a fresh WorkOS Organization, store the id, then proceed.
	workosOrgID := ""
	if cfg != nil {
		workosOrgID = cfg.OrganizationID
	}
	var err error
	if workosOrgID == "" {
		workosOrgID, err = client.createOrganization(ctx, orgID)
		if err != nil {
			return "", fmt.Errorf("workos org create: %w", err)
		}
	}

	link, err := client.generatePortalLink(ctx, workosOrgID, returnURL)
	if err != nil {
		return "", fmt.Errorf("workos portal link: %w", err)
	}

	now := time.Now()
	_ = s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.UpsertOrgSSO(ctx, &OrgSSOConfig{
			OrgID:          orgID,
			Provider:       "workos",
			OrganizationID: workosOrgID,
			Status:         "linked",
			ConfiguredAt:   &now,
		})
	})
	s.emit(ctx, actorID, "user", "sso.setup.started", "organization", orgID, orgID)
	return link, nil
}

// DisableSSO marks the org as not-SSO. We keep the WorkOS connection
// info around (organization_id) so a re-enable is one click — no
// need to walk through the portal again. Status flips to "disabled".
//
// actorID — the platform/org admin who initiated the disable.
func (s *Service) DisableSSO(ctx context.Context, actorID, orgID string) error {
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		cfg, err := s.store.GetOrgSSO(ctx, orgID)
		if err != nil {
			return err
		}
		if cfg == nil {
			return nil // idempotent
		}
		cfg.Status = "disabled"
		cfg.ConnectionID = ""
		return s.store.UpsertOrgSSO(ctx, cfg)
	}); err != nil {
		return err
	}
	s.emit(ctx, actorID, "user", "sso.disabled", "organization", orgID, orgID)
	return nil
}

// workosClient — minimal HTTP wrapper for the two REST endpoints we
// need (createOrganization, generatePortalLink). We avoid the full
// workos-go SDK to keep transitive deps tiny — same reasoning as the
// minio-go choice for audit-export.
type workosClient struct {
	apiKey string
	http   *http.Client
}

const workosBase = "https://api.workos.com"

// createOrganization mints a WorkOS Organization for the
// saas-starter org. The `external_id` field stamps our org id on
// theirs so subsequent webhooks can be routed back. Allowed
// authentication methods include all OIDC / SAML — the customer
// picks which IdP they wire in the portal.
func (c *workosClient) createOrganization(ctx context.Context, externalID string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"name":                                externalID,
		"external_id":                         externalID,
		"allow_profiles_outside_organization": false,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		workosBase+"/organizations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("workos org create: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("workos org create: empty id")
	}
	return out.ID, nil
}

// generatePortalLink mints a one-shot URL the operator's browser
// follows to configure their IdP. WorkOS docs: 15-min default TTL,
// re-callable to refresh.
func (c *workosClient) generatePortalLink(ctx context.Context, workosOrgID, returnURL string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"organization": workosOrgID,
		"intent":       "sso",
		"return_url":   returnURL,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		workosBase+"/portal/generate_link", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("portal link: status %d", resp.StatusCode)
	}
	var out struct {
		Link string `json:"link"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Link == "" {
		return "", fmt.Errorf("portal link: empty")
	}
	return out.Link, nil
}
