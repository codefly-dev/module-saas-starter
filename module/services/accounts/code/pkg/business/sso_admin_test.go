package business_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"accounts/pkg/business"
)

// ssoFakeStore is a partial fake — embeds the Store interface (panics
// on any unimplemented method, catching unexpected store calls) and
// overrides only the SSO methods.
type ssoFakeStore struct {
	business.Store
	mu        sync.Mutex
	cfg       map[string]*business.OrgSSOConfig
	upsertErr error
}

func newSSOFakeStore() *ssoFakeStore {
	return &ssoFakeStore{cfg: map[string]*business.OrgSSOConfig{}}
}

func (f *ssoFakeStore) GetOrgSSO(_ context.Context, orgID string) (*business.OrgSSOConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg[orgID], nil
}

func (f *ssoFakeStore) UpsertOrgSSO(_ context.Context, cfg *business.OrgSSOConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	cp := *cfg
	f.cfg[cp.OrgID] = &cp
	return nil
}

func TestStartSSOSetup_StubModeFailsWhenStateCannotBePersisted(t *testing.T) {
	store := newSSOFakeStore()
	store.upsertErr = errors.New("database unavailable")
	svc := newSSOService(store)

	got, err := svc.StartSSOSetup(context.Background(), "actor-platform-admin", "org-1", "https://app/admin/sso")
	if got != "" {
		t.Fatalf("portal URL: got %q, want empty URL on persistence failure", got)
	}
	if err == nil || err.Error() != "persist stub SSO setup: database unavailable" {
		t.Fatalf("error: got %v, want persistence failure", err)
	}
}

// WithOrgTx / WithControlPlane — pass-through. The real store wraps SQL
// reads/writes in a Postgres tx with SET LOCAL settings; this fake
// has no DB, so the wrapper is a no-op that just runs fn directly.
// Without these overrides, the embedded-nil Store panics when
// Service.StartSSOSetup calls them.
func (f *ssoFakeStore) WithOrgTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (f *ssoFakeStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func newSSOService(store business.Store) *business.Service {
	svc, _ := business.NewService(store)
	return svc
}

// TestStartSSOSetup_StubMode pins the dev/no-management-provider behaviour:
// a placeholder URL is returned (return_url + ?demo=1) and the org's
// SSO row is stamped with status=linked. Without this stub-path
// behaviour, every dev session on the SSO page would error with
// "identity management API key missing" — the whole point of the stub is that
// operators can exercise the FE flow with no creds wired.
func TestStartSSOSetup_StubMode(t *testing.T) {
	store := newSSOFakeStore()
	svc := newSSOService(store)

	got, err := svc.StartSSOSetup(context.Background(), "actor-platform-admin", "org-1", "https://app/admin/sso")
	if err != nil {
		t.Fatalf("StartSSOSetup error: %v", err)
	}
	want := "https://app/admin/sso?demo=1"
	if got != want {
		t.Errorf("portal URL: got %q, want %q", got, want)
	}
	row := store.cfg["org-1"]
	if row == nil {
		t.Fatal("expected SSO row written, got nil")
	}
	if row.Status != "linked" {
		t.Errorf("status: got %q, want linked", row.Status)
	}
	if row.Provider != "workos" {
		t.Errorf("provider: got %q, want workos", row.Provider)
	}
}

// TestDisableSSO_Idempotent — calling Disable on an org that has no
// SSO row is a no-op (not an error). Important for the "operator
// double-clicked Disable" UX.
func TestDisableSSO_Idempotent(t *testing.T) {
	store := newSSOFakeStore()
	svc := newSSOService(store)
	if err := svc.DisableSSO(context.Background(), "actor-platform-admin", "no-such-org"); err != nil {
		t.Fatalf("DisableSSO on missing row should be no-op, got %v", err)
	}
}

// TestDisableSSO_PreservesOrganizationID — when the operator disables
// SSO and re-enables, we DON'T want them to walk through portal
// setup again. Storage keeps the WorkOS organization_id; only the
// connection_id is cleared and status is flipped to disabled.
func TestDisableSSO_PreservesOrganizationID(t *testing.T) {
	store := newSSOFakeStore()
	store.cfg["org-1"] = &business.OrgSSOConfig{
		OrgID:          "org-1",
		Provider:       "workos",
		OrganizationID: "workos-org-abc",
		ConnectionID:   "conn-xyz",
		Status:         "active",
	}
	svc := newSSOService(store)

	if err := svc.DisableSSO(context.Background(), "actor-platform-admin", "org-1"); err != nil {
		t.Fatalf("DisableSSO error: %v", err)
	}
	row := store.cfg["org-1"]
	if row.Status != "disabled" {
		t.Errorf("status: got %q, want disabled", row.Status)
	}
	if row.ConnectionID != "" {
		t.Errorf("connection_id should be cleared, got %q", row.ConnectionID)
	}
	if row.OrganizationID != "workos-org-abc" {
		t.Errorf("organization_id should be preserved for re-enable, got %q", row.OrganizationID)
	}
}
