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
	// bootPending: the core is built and its boot reconciliation waits for
	// the reconcile pass that derives the module registry it reads
	// (completeAuthorityCoreBoot).
	bootPending bool
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
// once. Then the core's boot reconciliation runs (reconcileAuthorityCoreBoot).
//
// That reconciliation reads every module installation's consent through the
// module registry (moduleConsent), which a reconcile pass derives and which a
// host starts without. Run before that pass, it is refused for every consent
// ("the registry was derived from envelope revision 0"), so on a host whose
// pass derives the registry it waits for that pass: the pass runs it once the
// registry is derived and every later pass retries it until it completes
// (completeAuthorityCoreBoot). An error with the core built (AuthorityCore
// answers it) is a reconciliation that did not complete; it never stops the
// host.
func (s *Service) ConfigureAuthorityCore(ctx context.Context, issuer, keyID string, key ed25519.PrivateKey) error {
	state := s.authorityCoreState()
	core, err := s.newAuthorityCore(issuer, keyID, key)
	waits := err == nil && s.registryPassTrigger != nil && s.modulePrincipals.Load() == nil
	state.mu.Lock()
	state.core, state.err, state.bootPending = core, err, waits
	state.mu.Unlock()
	if err != nil {
		return err
	}
	if waits {
		// A pass that derived the registry before bootPending was set did
		// not run it; this one will.
		s.requestRegistryPass()
		return nil
	}
	return errors.Join(s.reconcileAuthorityCoreBoot(ctx, core), s.KeepAuthorityCoreCurrent(ctx))
}

// reconcileAuthorityCoreBoot is what the core reconciles once per start. A
// consent recorded before its reach was kept adopts, once, what its approved
// roles reach as the composition stands (installation.reach_adopted); until
// then the core holds it to that same reach, and every delivery adopts before
// it changes the composition. Then every applied delivery is composed in the
// core, so a module delivered before this host decided through the core, or
// while its core could not be built, is composed there too.
func (s *Service) reconcileAuthorityCoreBoot(ctx context.Context, core *authority.Service) error {
	var failures []error
	if err := core.AdoptLegacyReach(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: adopt the reach of earlier consents: %w", err))
	}
	if err := s.reconcileAuthorityCore(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: compose the applied deliveries: %w", err))
	}
	return errors.Join(failures...)
}

// completeAuthorityCoreBoot runs the core's boot reconciliation a pass waits
// for, once the pass has derived the module registry; until it completes,
// every pass retries it.
func (s *Service) completeAuthorityCoreBoot(ctx context.Context) error {
	state := s.authorityCoreState()
	state.mu.RLock()
	core, pending := state.core, state.bootPending
	state.mu.RUnlock()
	if core == nil || !pending {
		return nil
	}
	if err := s.reconcileAuthorityCoreBoot(ctx, core); err != nil {
		return err
	}
	state.mu.Lock()
	state.bootPending = false
	state.mu.Unlock()
	return nil
}

// KeepAuthorityCoreCurrent is what boot and every delivery pass run once the
// applied deliveries are composed: every declaration a live consent approved
// that the store does not keep yet is written where it can be rebuilt and is
// what was consented to (keepApprovedDeclarations), and every composed module
// is installed in every organization that does not hold it yet
// (InstallComposedModules). Both are idempotent.
func (s *Service) KeepAuthorityCoreCurrent(ctx context.Context) error {
	var failures []error
	if err := s.keepApprovedDeclarations(ctx); err != nil {
		failures = append(failures, fmt.Errorf("authority core: keep the approved declarations: %w", err))
	}
	if err := s.InstallComposedModules(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// WorkContextMaxTTL is the longest any Work Context this host issues lives,
// the authority core's own ceiling (authority.DefaultMaxTTL), not the Work
// Context library's default of an hour. Removing a grant moves a revision and
// ends what was issued under it at the next check; the lifetime is what bounds
// the cases no revision reaches (a grant that expires, a token that leaks).
// The gateway hop renews a held context before it lapses, so a person sees no
// difference; a task that outlives it asks again.
const WorkContextMaxTTL = authority.DefaultMaxTTL

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
		Replay: store.AuthorityCoreReplay(), MaxTTL: WorkContextMaxTTL, Journal: s.journalAuthority,
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

// ComposedAudience is the module the core composed under audience, the
// principal every capability to it names, read as the core reads an audience
// (IssueForPerson reads its module by principal, not by its delivery's name).
// A module the core does not compose is domain.ErrNotFound; a host that
// builds no core answers ErrAuthorityCoreUnconfigured.
func (s *Service) ComposedAudience(ctx context.Context, audience string) (domain.Module, error) {
	if _, err := s.AuthorityCore(); err != nil {
		return domain.Module{}, err
	}
	store, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return domain.Module{}, ErrAuthorityCoreUnconfigured
	}
	var m domain.Module
	err := store.AuthorityCoreWithConsent(s.moduleConsent).Read(ctx, func(tx authority.Tx) (err error) {
		m, err = tx.Module(audience)
		return err
	})
	return m, err
}

// ServesModule reports whether this host serves a module under prefix: one
// its registry carries, or one it declares (a declared module's alias, as the
// host's audience vocabulary admits it, HostAudiences), which the registry
// omits while its envelope does not name it or while it is fenced. It is an
// existence guard, not a capability decision: it reads the registry through
// ModulePrincipals, the accessor for callers that inspect the declaration.
func (s *Service) ServesModule(ctx context.Context, prefix string) (bool, error) {
	if _, served := s.ModulePrincipals()[ModulePrincipalID(prefix)]; served {
		return true, nil
	}
	bindings, err := s.liveDeclaredBindings(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(bindings, func(b LiveDeclaredBinding) bool {
		return b.Kind == SolutionDeclaredKindModule && strings.TrimSpace(b.Alias) == prefix
	}), nil
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
