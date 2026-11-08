package business

// The host interfaces are decided by the shared authority core
// (codefly-dev/authority libraries/authority-core), the same one the local
// authority runs. This file builds it over this host's store and signing key;
// accounts keeps what is its own: identity, the signing key, delivery, the
// review of workload credentials, and the audit journal.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/workcontext"

	"accounts/pkg/permissioncatalog"
)

// AuthorityCoreStore is a store that keeps the authority core's records.
type AuthorityCoreStore interface {
	AuthorityCoreWithConsent(ModuleConsent) authority.Store
	AuthorityCoreReplay() workcontext.ReplayStore
}

// ModuleConsent answers, for one module installation's recorded consent (its
// installations.scope) and the envelope revision committed in the reading
// transaction, whether the installation allows any capability and what it
// allows of the module's operation bindings. The authority core bounds every
// issue through a binding by it (domain.Installation.Bindings).
type ModuleConsent func(prefix string, consent []byte, committedEnvelope uint64) (bindings map[string]domain.Binding, active bool, err error)

// ErrAuthorityCoreUnconfigured reports a host interface call this host cannot
// decide: the authority core is not built (no signing key, or a store that
// keeps no core records).
var ErrAuthorityCoreUnconfigured = errors.New("the authority core is not configured")

type authorityCoreState struct {
	mu   sync.RWMutex
	core *authority.Service
	err  error
	// step overrides the host's durable setup adapter for explicit test/host
	// integration. An absent override uses canonicalInstallationStep.
	step authority.InstallStep
}

func (s *Service) authorityCoreState() *authorityCoreState {
	if s.authorityCore == nil {
		s.authorityCore = &authorityCoreState{err: ErrAuthorityCoreUnconfigured}
	}
	return s.authorityCore
}

type staticKeys map[string]ed25519.PublicKey

func (k staticKeys) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) { return k, nil }

// ConfigureAuthorityCore builds the core over this host's store, signing as
// issuer with key under keyID: the Work Context key, so every verifier pinning
// Keys verifies what the core issues. A module is installed at an organization
// only by accounts' own consent (an administrator's install, within the module
// envelope): the core installs none itself and reads accounts' records live
// (HostInstallsModules), so an uninstall or a narrowed ceiling ends it there at
// once. Consents recorded before reach was kept adopt their reach
// (AdoptLegacyReach), every applied delivery is composed in the core, then
// the pass every delivery pass repeats runs (KeepAuthorityCoreCurrent). An error with the
// core built (AuthorityCore answers it) is a reconciliation that did not
// complete, which the next delivery pass retries; it never stops the host.
func (s *Service) ConfigureAuthorityCore(ctx context.Context, issuer, keyID string, key ed25519.PrivateKey) error {
	state := s.authorityCoreState()
	core, err := s.newAuthorityCore(issuer, keyID, key)
	state.mu.Lock()
	state.core, state.err = core, err
	state.mu.Unlock()
	if err != nil {
		return err
	}
	var failures []error
	// A consent recorded before its reach was kept adopts, once, what its
	// approved roles reach as the composition stands when the host starts
	// (installation.reach_adopted); until then the core holds it to that
	// same reach. The core adopts before any delivery changes the
	// composition too, so this only covers a host that starts on one.
	if err := core.AdoptLegacyReach(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: adopt the reach of earlier consents: %w", err))
	}
	if err := s.reconcileAuthorityCore(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: compose the applied deliveries: %w", err))
	}
	if err := s.KeepAuthorityCoreCurrent(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// KeepAuthorityCoreCurrent is what boot and every delivery pass run once the
// applied deliveries are composed: every declaration a live consent approved
// that the store does not keep yet is written where it can be rebuilt and is
// what was consented to (keepApprovedDeclarations). It is idempotent.
func (s *Service) KeepAuthorityCoreCurrent(ctx context.Context) error {
	if err := s.keepApprovedDeclarations(ctx); err != nil {
		return fmt.Errorf("authority core: keep the approved declarations: %w", err)
	}
	return nil
}

func (s *Service) newAuthorityCore(issuer, keyID string, key ed25519.PrivateKey) (*authority.Service, error) {
	store, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return nil, fmt.Errorf("%w: the store keeps no authority core records", ErrAuthorityCoreUnconfigured)
	}
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(keyID) == "" || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: an issuer, a key id and an Ed25519 signing key are required", ErrAuthorityCoreUnconfigured)
	}
	public, _ := key.Public().(ed25519.PublicKey)
	return authority.New(store.AuthorityCoreWithConsent(s.moduleConsent), authority.Config{
		Issuer: issuer, KeyID: keyID, Signer: key, Keys: staticKeys{keyID: public},
		Replay: store.AuthorityCoreReplay(), MaxTTL: workcontext.DefaultMaxTTL, Journal: s.journalAuthority,
		HostInstallsModules: true,
	})
}

// AuthorityCore is the core every host interface operation is decided by.
func (s *Service) AuthorityCore() (*authority.Service, error) {
	state := s.authorityCoreState()
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.core == nil {
		return nil, state.err
	}
	return state.core, nil
}

// SetAuthorityInstallStep sets the step a solution installation runs before
// it is active; nil restores the host's durable setup adapter.
func (s *Service) SetAuthorityInstallStep(step authority.InstallStep) {
	state := s.authorityCoreState()
	state.mu.Lock()
	state.step = step
	state.mu.Unlock()
}

// AuthorityInstallStep is the step a solution installation runs.
func (s *Service) AuthorityInstallStep() authority.InstallStep {
	state := s.authorityCoreState()
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.step != nil {
		return state.step
	}
	return s.canonicalInstallationStep
}

// MemberPermissions are the contributed permissions every current member of
// an organization holds as a person (a contribution's `members: true`),
// sorted by resource and action.
func MemberPermissions() []permissioncatalog.Permission {
	var out []permissioncatalog.Permission
	for _, p := range permissioncatalog.Permissions() {
		if p.Members {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b permissioncatalog.Permission) int {
		return strings.Compare(a.Resource+":"+a.Action, b.Resource+":"+b.Action)
	})
	return out
}
