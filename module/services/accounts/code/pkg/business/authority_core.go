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
	"github.com/codefly-dev/core/workcontext"

	"accounts/pkg/permissioncatalog"
)

// AuthorityCoreStore is a store that keeps the authority core's records.
type AuthorityCoreStore interface {
	AuthorityCore() authority.Store
	AuthorityCoreReplay() workcontext.ReplayStore
}

// ErrAuthorityCoreUnconfigured reports a host interface call this host cannot
// decide: the authority core is not built (no signing key, or a store that
// keeps no core records).
var ErrAuthorityCoreUnconfigured = errors.New("the authority core is not configured")

type authorityCoreState struct {
	mu   sync.RWMutex
	core *authority.Service
	err  error
	// step is the install step a solution installation runs before it is
	// active; nil activates at once, as the local authority does.
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
// once.
func (s *Service) ConfigureAuthorityCore(ctx context.Context, issuer, keyID string, key ed25519.PrivateKey) error {
	state := s.authorityCoreState()
	core, err := s.newAuthorityCore(issuer, keyID, key)
	state.mu.Lock()
	state.core, state.err = core, err
	state.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.reconcileAuthorityCore(ctx); err != nil {
		return fmt.Errorf("authority core: compose the applied deliveries: %w", err)
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
	return authority.New(store.AuthorityCore(), authority.Config{
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
// it is active; nil activates it at once.
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
	return state.step
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
