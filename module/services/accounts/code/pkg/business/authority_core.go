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
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/core/wool"
	"github.com/codefly-dev/core/workcontext"

	gen "accounts/pkg/gen/saas/accounts/v1"
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
// once. Every applied delivery is composed in the core, then the pass every
// delivery pass repeats runs (KeepAuthorityCoreCurrent). An error with the
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
// what was consented to (keepApprovedDeclarations), and every module
// installation that needs its review is reported (reportUnreviewedModulePages).
// Both are idempotent.
func (s *Service) KeepAuthorityCoreCurrent(ctx context.Context) error {
	var failures []error
	if err := s.keepApprovedDeclarations(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: keep the approved declarations: %w", err))
	}
	if err := s.reportUnreviewedModulePages(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// unreviewedModulePages lists the active module installations of this host's
// own (installed by name: no principal, not the core's) at org, or at every
// organization when org is "", whose composed target is now role-gated
// (decide.RoleGated). A role-gated target's entitlement is read from the
// core's record alone, so such an installation entitles nobody, and it cannot
// be installed again through its review without orphaning what it was
// granted: it needs an administrator to uninstall it and install the target
// through its review. Delivery refuses to make a module role-gated while one
// exists (refuseRoleGatingByName); one that predates that refusal is reported
// here, never a reason not to start.
func (s *Service) unreviewedModulePages(ctx context.Context, org string) ([]domain.Installation, error) {
	cores, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return nil, nil
	}
	var found []domain.Installation
	// No consent reading: the installation's own status decides, not what its
	// consent allows today.
	err := cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
		found = nil
		all, err := tx.Installations(org)
		if err != nil {
			return err
		}
		composed := map[string]domain.Module{}
		for _, inst := range all {
			if inst.PrincipalID != "" || inst.Status != domain.Active {
				continue
			}
			m, seen := composed[inst.Target]
			if !seen {
				if m, err = tx.ModuleNamed(inst.Target); err != nil && !errors.Is(err, domain.ErrNotFound) {
					return err
				}
				composed[inst.Target] = m
			}
			if m.Name != "" && m.Kind != domain.KindSolution && decide.RoleGated(m) {
				found = append(found, inst)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("authority core: read the module installations: %w", err)
	}
	return found, nil
}

// reportUnreviewedModulePages logs, at error level, each module installation
// that needs its review (unreviewedModulePages): once per call, so once per
// boot and per delivery pass. The installation read states the same as an
// unmet readiness condition.
func (s *Service) reportUnreviewedModulePages(ctx context.Context) error {
	found, err := s.unreviewedModulePages(ctx, "")
	if err != nil {
		return err
	}
	log := wool.Get(ctx).In("reportUnreviewedModulePages")
	for _, inst := range found {
		log.Error("a module declaring roles is installed by name, not through its review, so it entitles nobody; an organization administrator must uninstall it, then install it through its review",
			wool.Field("installation", inst.ID), wool.Field("organization", inst.Organization), wool.Field("target", inst.Target))
	}
	return nil
}

// unreviewedModulePageCondition is the readiness an installation that needs
// its review (unreviewedModulePages) reads with.
func unreviewedModulePageCondition(target string) ReadinessCondition {
	return ReadinessCondition{
		Kind:    gen.ReadinessConditionKind_READINESS_CONDITION_KIND_RECORD_UNHEALTHY,
		Subject: target,
		Detail: fmt.Sprintf("%s now declares roles, and this installation was made by name, not through its review, so it entitles nobody. "+
			"An organization administrator clears it: uninstall it, then install it through its review.", target),
		ClearedBy: gen.ReadinessClearedBy_READINESS_CLEARED_BY_ORGANIZATION_ADMIN,
		Action:    gen.ReadinessAction_READINESS_ACTION_NONE_AT_THIS_SURFACE,
	}
}

// refuseRoleGatingByName refuses, as a delivery conflict, a generation of m
// that makes a module role-gated (it was not, or was not composed) while an
// active installation of it made by name stands: that installation would
// entitle nobody, and could not be installed again through its review without
// orphaning what it was granted. The refusal names each one; an organization
// administrator uninstalls them, then the generation is delivered again.
func (s *Service) refuseRoleGatingByName(ctx context.Context, m domain.Module) error {
	cores, ok := s.store.(AuthorityCoreStore)
	if !ok || m.Kind == domain.KindSolution || !decide.RoleGated(m) {
		return nil
	}
	var found []string
	err := cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
		found = nil
		if held, err := tx.ModuleNamed(m.Name); err == nil && decide.RoleGated(held) {
			return nil // role-gated already: this generation does not make it so
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		all, err := tx.Installations("")
		if err != nil {
			return err
		}
		for _, inst := range all {
			if inst.Target == m.Name && inst.PrincipalID == "" && inst.Status == domain.Active {
				found = append(found, fmt.Sprintf("installation %s at organization %s", inst.ID, inst.Organization))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("authority core: read the module installations: %w", err)
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("%w: generation %d of %s declares roles while it is installed by name (%s); an organization administrator uninstalls each, then the generation is delivered again and %s is installed through its review",
		ErrSolutionDeliveryConflict, m.Generation, m.Name, strings.Join(found, "; "), m.Name)
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
