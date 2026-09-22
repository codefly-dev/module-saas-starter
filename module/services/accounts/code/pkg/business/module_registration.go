package business

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/codefly-dev/core/wool"
)

// Federation-registration credential issuance.
//
// A composed module federates its REST prefix with the gateway by presenting a
// signed, prefix-bound token; a solution registers its gateway upstream and its
// frontend Module-Federation remote by presenting a signed, solution-bound one.
// This service is the authority that issues both: the gateway already trusts
// this cluster's Ed25519 key, and the decision "may this caller own that
// identity" is answered here rather than asserted by the registrant.
//
// The caller proves WHICH registrant it is with its own registration secret,
// provisioned by the composition alongside the digest declared here. A module
// holding the credential for "documents" therefore cannot obtain a token for
// "billing": the secret is compared against the entry for the identity it asks
// for, and there is no entry a shared cluster-wide credential would satisfy.
//
// The two kinds are declared in SEPARATE configuration keys and minted under
// separate audiences even though the mechanism is identical. A solution's
// remote executes JavaScript in the host origin with the viewer's credentials,
// so its credential is the strictly larger grant; letting a module secret reach
// it would widen authority by accident.

// ErrModuleRegistrationDenied is returned for every module refusal — unknown
// prefix, wrong secret, unconfigured registrar — so a caller probing the surface
// cannot tell which modules a composition declared.
var ErrModuleRegistrationDenied = errors.New("module registration denied")

// ErrSolutionRegistrationDenied is the solution counterpart, and is equally
// undiscriminating about why.
var ErrSolutionRegistrationDenied = errors.New("solution registration denied")

// registrationIdentityPattern mirrors the gateway's catalog-identity rule: one
// lowercase DNS-ish segment, no slashes, wildcards, or traversal. It governs
// module prefixes and solution ids alike — both name a single routing segment.
var registrationIdentityPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// ModuleRegistrationMinter is the narrow signing capability issuance needs: no
// session store, no refresh rotation, no identity.
type ModuleRegistrationMinter interface {
	MintModuleRegistration(prefix string) (string, time.Time, error)
}

// SolutionRegistrationMinter signs the solution-audience counterpart.
type SolutionRegistrationMinter interface {
	MintSolutionRegistration(solutionID string) (string, time.Time, error)
}

// registrationAuthority holds the composition-declared policy for one registrant
// kind and the signing capability for it. A nil authority denies everything: a
// deployment that has not said who may register must not let anyone.
type registrationAuthority struct {
	declared func() map[string][sha256.Size]byte
	mint     func(id string) (string, time.Time, error)
	denied   error
}

// fixedDeclaration is the policy of a registrant kind whose declaration is
// composed into the host build and therefore cannot change under a running
// process.
func fixedDeclaration(secrets map[string][sha256.Size]byte) func() map[string][sha256.Size]byte {
	return func() map[string][sha256.Size]byte { return secrets }
}

// declarationReader resolves a declaration that can change under a running
// process. It reads the raw value on every call — that is the whole point — but
// parses it only when the value actually changes, and reports a declaration
// that stopped parsing, and its later recovery, once per state rather than once
// per exchange.
//
// Both properties come from the same place, which is why they are one type. A
// malformed declaration is STICKY: it stays malformed until an operator fixes
// it, while every registrant keeps retrying on its own beat. Re-parsing per
// exchange would burn the parse on an unchanged string and emit one identical
// warning per beat per registrant for as long as the misconfiguration lasts,
// burying the one line that says what is wrong. Keying on the raw value makes
// "this is the same state" the same fact that makes the parse unnecessary.
type declarationReader struct {
	read func() string

	mu      sync.Mutex
	raw     string
	loaded  bool
	secrets map[string][sha256.Size]byte
	broken  bool
}

// declared returns the identities the declaration currently authorizes. A
// declaration that does not parse authorizes nobody: a nil map declares no
// identity, so every comparison against it fails closed exactly as an empty
// declaration does.
func (r *declarationReader) declared() map[string][sha256.Size]byte {
	// Read outside the lock, so concurrent exchanges do not serialize on it.
	// Two callers straddling an edit can therefore store their results out of
	// order, leaving the cache holding the older string. That costs one extra
	// parse on the next call, which corrects it, and cannot decide anything
	// wrongly: each caller authorizes against the value IT read, which is the
	// strongest thing true at the moment it asked.
	raw := r.read()

	r.mu.Lock()
	if r.loaded && raw == r.raw {
		secrets := r.secrets
		r.mu.Unlock()
		return secrets
	}
	secrets, err := ParseRegistrationSecrets(raw)
	recovered := err == nil && r.broken
	r.raw, r.loaded, r.secrets, r.broken = raw, true, secrets, err != nil
	r.mu.Unlock()

	// Reported outside the lock, and only on a state change, so exactly one
	// caller can ever report a given transition.
	switch {
	case err != nil:
		wool.Get(context.Background()).In("declarationReader.declared").Warn(
			"registration declaration no longer parses; denying every registration until it is corrected",
			wool.ErrField(err))
	case recovered:
		wool.Get(context.Background()).In("declarationReader.declared").Info(
			"registration declaration parses again; registrations are no longer denied for it")
	}
	return secrets
}

// authorize reports whether `secret` is the declared credential for `id`,
// against the declaration as it stands now.
//
// Comparison runs the same way whether or not the identity was declared: an
// undeclared identity compares against a zero digest rather than returning
// early, so response time does not reveal what a composition declared.
// `declared` is still required, so the zero digest can never authenticate on its
// own. Resolving the declaration first costs the same for every id — and, for a
// declaration that has not changed, does not scale with how many identities it
// holds — so it leaves that property intact.
func (a *registrationAuthority) authorize(id, secret string) bool {
	expected, declared := a.declared()[id]
	presented := sha256.Sum256([]byte(secret))
	matched := subtle.ConstantTimeCompare(expected[:], presented[:]) == 1
	return declared && secret != "" && matched
}

// SetModuleRegistrar wires module-registration issuance. Called once at startup
// with the composition's declared secrets; leaving it unset denies every
// registration. A module is composed into this host's build, so its declaration
// cannot change without a new deployment and is held as parsed.
func (s *Service) SetModuleRegistrar(minter ModuleRegistrationMinter, secrets map[string][sha256.Size]byte) {
	s.moduleRegistrar = &registrationAuthority{
		declared: fixedDeclaration(secrets),
		mint:     minter.MintModuleRegistration,
		denied:   ErrModuleRegistrationDenied,
	}
}

func (s *Service) SetModuleIdentitySecrets(secrets map[string][sha256.Size]byte) {
	s.moduleIdentity = &registrationAuthority{declared: fixedDeclaration(secrets)}
}

// SetSolutionRegistrar wires solution-registration issuance, from the separate
// declaration that governs who may publish a host-origin remote.
//
// `declaration` is read on EVERY exchange rather than parsed once here. A
// solution is not composed into this host: it mounts by registering against a
// host that is already serving, which is what makes it addable, replaceable and
// removable without redeploying the host. Holding the parsed allowlist for the
// process lifetime broke exactly that — authorizing a publisher took effect only
// at the next restart of this service, and withdrawing one likewise kept minting
// until then.
func (s *Service) SetSolutionRegistrar(minter SolutionRegistrationMinter, declaration func() string) {
	reader := &declarationReader{read: declaration}
	s.solutionRegistrar = &registrationAuthority{
		declared: reader.declared,
		mint:     minter.MintSolutionRegistration,
		denied:   ErrSolutionRegistrationDenied,
	}
}

// ParseRegistrationSecrets reads one composition-declared set of registration
// credentials, formatted as comma-separated `identity:sha256hex` entries. Only
// the digest is configured here; the registrant holds the plaintext. Both
// registrant kinds are declared in this form, in their own configuration key.
func ParseRegistrationSecrets(raw string) (map[string][sha256.Size]byte, error) {
	secrets := map[string][sha256.Size]byte{}
	for position, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, digest, ok := strings.Cut(entry, ":")
		if !ok {
			// Located, not quoted: an entry with no separator is most often a
			// plaintext secret pasted where its digest belongs, and this error
			// reaches a log.
			return nil, fmt.Errorf("registration secret at position %d is not identity:sha256hex", position+1)
		}
		prefix = strings.TrimSpace(prefix)
		if !registrationIdentityPattern.MatchString(prefix) || len(prefix) > 63 {
			return nil, fmt.Errorf("registration secret has invalid identity %q", prefix)
		}
		decoded, err := hex.DecodeString(strings.TrimSpace(digest))
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("registration secret for %q is not a sha256 hex digest", prefix)
		}
		if _, duplicate := secrets[prefix]; duplicate {
			return nil, fmt.Errorf("registration secret for %q declared twice", prefix)
		}
		secrets[prefix] = [sha256.Size]byte(decoded)
	}
	return secrets, nil
}

// ModuleMintRegistration authorizes a module against its declared registration
// secret and issues the gateway credential. Every refusal returns
// ErrModuleRegistrationDenied.
func (s *Service) ModuleMintRegistration(ctx context.Context, prefix, secret string) (string, time.Time, error) {
	if s.moduleRegistrar == nil {
		return "", time.Time{}, ErrModuleRegistrationDenied
	}
	token, expiresAt, err := s.moduleRegistrar.authorizeAndMint(prefix, secret)
	if err != nil {
		return "", time.Time{}, err
	}
	// Issuing a credential that grants control over request routing is a
	// security event with no authenticated edge call behind it to carry the
	// record, so this hop emits its own — and refuses to hand the credential out
	// when the record cannot be committed.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, "module:"+prefix, "system", EventModuleRegistrationMint, "module", prefix, "",
			map[string]any{"prefix": prefix})
	}); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// SolutionMintRegistration authorizes a solution against its declared
// registration secret and issues the credential the gateway and the frontend
// both accept. Every refusal returns ErrSolutionRegistrationDenied.
//
// The record is emitted here rather than in the shared path both kinds run,
// because an emit site that names its event through a field is one whose
// durability the audit gate cannot check. Each kind states its own constant.
func (s *Service) SolutionMintRegistration(ctx context.Context, solutionID, secret string) (string, time.Time, error) {
	if s.solutionRegistrar == nil {
		return "", time.Time{}, ErrSolutionRegistrationDenied
	}
	token, expiresAt, err := s.solutionRegistrar.authorizeAndMint(solutionID, secret)
	if err != nil {
		return "", time.Time{}, err
	}
	// Same reasoning as the module hop, over strictly more authority: this
	// credential also decides what executes in the host origin.
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, "solution:"+solutionID, "system", EventSolutionRegistrationMint, "solution", solutionID, "",
			map[string]any{"solution_id": solutionID})
	}); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// authorizeAndMint is the one path both registrant kinds run: validate the
// identity shape, authenticate the presented secret against the declaration for
// exactly that identity, and sign.
func (a *registrationAuthority) authorizeAndMint(id, secret string) (string, time.Time, error) {
	if a.mint == nil || !registrationIdentityPattern.MatchString(id) || !a.authorize(id, secret) {
		return "", time.Time{}, a.denied
	}
	return a.mint(id)
}

// ModuleWorkContextAuthority is the identity one module Work Context mint
// asserts: who the module acts as, and on which tenant. The capabilities that
// identity may exercise are not sealed here — every call re-reads the declared
// grant, so narrowing a module's authority takes effect immediately rather than
// when its current token expires.
type ModuleWorkContextAuthority struct {
	PrincipalID string
	Tenant      string
}

// ModuleAuthorizeWorkContext resolves the identity a composed module may be
// issued a Work Context for. It authenticates with the identity secret,
// its principal is derived from the prefix that secret is bound to, and its
// tenant is the one the deployment declared — so a
// module can never name a tenant it was not granted by asking for it.
func (s *Service) ModuleAuthorizeWorkContext(prefix, secret string) (ModuleWorkContextAuthority, error) {
	authority := s.moduleIdentity
	if authority == nil ||
		!registrationIdentityPattern.MatchString(prefix) ||
		!authority.authorize(prefix, secret) {
		return ModuleWorkContextAuthority{}, ErrModuleRegistrationDenied
	}
	principalID := ModulePrincipalID(prefix)
	grant, registered := s.modulePrincipals[principalID]
	if !registered {
		return ModuleWorkContextAuthority{}, ErrModuleRegistrationDenied
	}
	return ModuleWorkContextAuthority{PrincipalID: principalID, Tenant: grant.Tenant}, nil
}

// ErrModuleTenantUnknown reports that a module principal's declared tenant
// names no organization in this database.
var ErrModuleTenantUnknown = errors.New("module principal tenant names no organization")

// VerifyModuleTenant confirms the tenant a grant declares actually exists
// before a capability is sealed to it.
//
// ParseModulePrincipalRegistry can only check the tenant's *form* — it runs at
// boot, from configuration, with no database to ask. Nothing after it checked
// either: ModuleAuthorizeWorkContext copies the value through (it takes no
// ctx, so it cannot ask), and authorizeTenant compares the sealed tenant
// against itself, which always passes. A tenant uuid that names no
// organization — a typo, or an id carried over from another environment —
// therefore signed cleanly and bound every call to a tenant nobody can see.
//
// That failure is silent all the way down: audit_events.org_id and
// domain_events.tenant_id are bare UUID columns with no foreign key to
// organizations, so the writes succeed and nothing ever raises. The mint is
// the last point that holds both the tenant and a database handle, so it is
// where the check belongs — and it closes every route at once, including
// configuration this module never generated.
func (s *Service) VerifyModuleTenant(ctx context.Context, authority ModuleWorkContextAuthority) error {
	var exists bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var checkErr error
		exists, checkErr = s.store.OrganizationIDExists(ctx, authority.Tenant)
		return checkErr
	}); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: principal %s declares tenant %s",
			ErrModuleTenantUnknown, authority.PrincipalID, authority.Tenant)
	}
	return nil
}

// RecordModuleWorkContextMint commits the durable record of an issued module
// capability. Like the registration credential it is written after the thing it
// describes exists, and the caller withholds the capability when the record
// cannot be committed — so the spine never carries an issuance that did not
// happen, and never misses one that did.
func (s *Service) RecordModuleWorkContextMint(
	ctx context.Context, prefix string, authority ModuleWorkContextAuthority,
) error {
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		return s.emitTx(ctx, authority.PrincipalID, "system", EventModuleWorkContextMint, "module", prefix,
			authority.Tenant, map[string]any{"prefix": prefix, "tenant": authority.Tenant})
	})
}
