package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// sealAnswersFake answers core's live questions — seal, epoch, execution,
// revision, the audience's installation — for adapter tests that are about
// something other than where those answers come from.
//
// It is a fake of the SOURCES and never of the check: every capability a test
// mints here is signed by core's Authority and verified by core's Verifier,
// exactly as the host does, against these answers.
type sealAnswersFake struct {
	mu sync.Mutex
	// revision is the authorization revision every owner answers, unless
	// revisions names one.
	revision  uint64
	revisions map[string]uint64
	// epochs overrides the epoch (default 1) per principal.
	epochs map[string]uint64
	// unknown names principals the organization has no record of.
	unknown map[string]bool
	// unheld names installations no owner holds any more.
	unheld map[string]bool
	// uninstalled names audiences the organization never installed.
	uninstalled map[string]bool
	// moduleRevision, when set, answers a module principal's revision from
	// the business service, so a module capability carries the revision a
	// consumer's CheckAuthorizationRevision confirms it against.
	moduleRevision func(ctx context.Context, orgID, ownerID string) (uint64, bool, error)
	// revisionFrom, when set, answers every person's revision live — for a
	// fixture whose authority facts a test replaces after the server is built.
	revisionFrom func() uint64
	// revisionErr refuses one owner's revision, which is how a removed member
	// is expressed: the row-backed read answers
	// ErrWorkContextPrincipalUnknown for a principal with no authorization
	// record, and that is a DECISION about the capability rather than a lookup
	// that failed.
	revisionErr map[string]error
	// tenants records the organization each answer was asked for, so a serving
	// path that dropped the tenant is a failing test rather than a fake that
	// never looked.
	tenants []string
}

func newSealAnswersFake(revision uint64) *sealAnswersFake {
	return &sealAnswersFake{
		revision:    revision,
		revisions:   map[string]uint64{},
		epochs:      map[string]uint64{},
		unknown:     map[string]bool{},
		unheld:      map[string]bool{},
		uninstalled: map[string]bool{},
		revisionErr: map[string]error{},
	}
}

// testInstallation is the installation the fake answers for an audience.
func testInstallation(audience string) string { return "installation-" + audience }

func (f *sealAnswersFake) WorkContextSeal(_ context.Context, orgID, _, installationID string) (string, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants = append(f.tenants, orgID)
	if f.unheld[installationID] {
		return "", 0, business.ErrWorkContextSealUnheld
	}
	return installationID, 1, nil
}

func (f *sealAnswersFake) WorkContextPrincipalEpoch(_ context.Context, orgID, principalID string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants = append(f.tenants, orgID)
	if f.unknown[principalID] {
		return 0, business.ErrWorkContextPrincipalUnknown
	}
	if epoch, ok := f.epochs[principalID]; ok {
		return epoch, nil
	}
	return 1, nil
}

func (f *sealAnswersFake) WorkContextRevision(ctx context.Context, orgID, ownerID string) (uint64, error) {
	if f.moduleRevision != nil {
		if revision, handled, err := f.moduleRevision(ctx, orgID, ownerID); handled {
			return revision, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants = append(f.tenants, orgID)
	if err, refused := f.revisionErr[ownerID]; refused {
		return 0, err
	}
	if revision, ok := f.revisions[ownerID]; ok {
		return revision, nil
	}
	if f.revisionFrom != nil {
		return f.revisionFrom(), nil
	}
	return f.revision, nil
}

func (f *sealAnswersFake) WorkContextAudienceInstallation(_ context.Context, _, audience string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uninstalled[audience] {
		return "", business.ErrWorkContextNoAudienceInstallation
	}
	return testInstallation(audience), nil
}

// askedTenants is the organization each answer was asked for, in order.
func (f *sealAnswersFake) askedTenants() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tenants...)
}

// refuseRevision makes one owner's revision read as a principal this
// organization has no authorization record for — a removed member.
func (f *sealAnswersFake) refuseRevision(ownerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revisionErr[ownerID] = business.ErrWorkContextPrincipalUnknown
}

// setRevision moves every owner's revision, which is how a test revokes.
func (f *sealAnswersFake) setRevision(revision uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revision = revision
}

// memoryReplay is a durable-replay double: one consumption per (org, nonce).
type memoryReplay struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (m *memoryReplay) ConsumeSingleUseWorkContext(_ context.Context, orgID, contextID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	key := orgID + "\x00" + contextID
	if m.seen[key] {
		return business.ErrWorkContextAlreadyConsumed
	}
	m.seen[key] = true
	return nil
}

func (m *memoryReplay) PurgeExpiredWorkContextReplays(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// newCoreTestServer configures a Work Context server against authority and
// seals, with a fresh signing key and an in-memory replay store when authority
// does not bring one.
func newCoreTestServer(t *testing.T, authority business.WorkContextAuthorityStore, seals *sealAnswersFake) *WorkContextAuthorityServer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  authority,
		Seals:      seals,
	})
	require.NoError(t, server.configureErr)
	if server.replay == nil {
		server.replay = &memoryReplay{}
	}
	return server
}

// testCapability describes a capability a test needs to hold, minted the way
// the host mints: the owner's session, then one Child per actor hop.
type testCapability struct {
	Audience string
	Tenant   string
	Owner    string
	// OwnerKind defaults to human.
	OwnerKind string
	Task      string
	// Session is the issuer-bound session; empty mints a fresh one.
	Session string
	// Replay defaults to idempotent.
	Replay string
	Scopes []*basev0.WorkScopeV1
	Actors []delegationHop
	TTL    time.Duration
	// Installation defaults to the fake's answer for the audience.
	Installation string
}

func mintTestCapability(t *testing.T, server *WorkContextAuthorityServer, c testCapability) (string, *basev0.WorkContextV1) {
	t.Helper()
	token, signed, err := tryMintTestCapability(server, c)
	require.NoError(t, err)
	return token, signed
}

func tryMintTestCapability(server *WorkContextAuthorityServer, c testCapability) (string, *basev0.WorkContextV1, error) {
	if c.OwnerKind == "" {
		c.OwnerKind = business.PrincipalKindHuman
	}
	if c.Task == "" {
		c.Task = uuid.NewString()
	}
	if c.Replay == "" {
		c.Replay = workcontext.ReplayIdempotent
	}
	if c.Installation == "" {
		c.Installation = testInstallation(c.Audience)
	}
	return server.mint(context.Background(), mintRequest{
		Start: workcontext.StartInput{
			TenantID:           c.Tenant,
			OwnerPrincipalID:   c.Owner,
			OwnerPrincipalKind: c.OwnerKind,
			OrganizationID:     c.Tenant,
			TaskID:             c.Task,
			Audience:           c.Audience,
			AuthorityScopes:    c.Scopes,
			InstallationID:     c.Installation,
		},
		Hops:    c.Actors,
		Session: c.Session,
		Replay:  c.Replay,
		TTL:     c.TTL,
	})
}

// agentHop is the registered-agent actor hop the tests delegate to.
func agentHop(principalID, delegationID string, scopes ...*basev0.WorkScopeV1) delegationHop {
	return delegationHop{
		PrincipalID:   principalID,
		PrincipalKind: business.PrincipalKindAgent,
		AgentID:       "agent.test/" + principalID,
		DelegationID:  delegationID,
		GrantedScopes: scopes,
	}
}

// moduleSealAnswers is sealAnswersFake for a test that mints module
// capabilities: a module principal's revision is the business service's.
func moduleSealAnswers(revision uint64) *sealAnswersFake {
	answers := newSealAnswersFake(revision)
	answers.moduleRevision = func(ctx context.Context, orgID, ownerID string) (uint64, bool, error) {
		if _, declared := moduleExecution.registry[ownerID]; !declared || service == nil {
			return 0, false, nil
		}
		revision, err := service.WorkContextRevision(ctx, orgID, ownerID)
		return revision, true, err
	}
	return answers
}

// mintModuleIdentity mints principalID's own identity capability in tenant
// through the shared issuer: the token a composed module forwards to the
// capability surface.
func mintModuleIdentity(t *testing.T, principalID, tenant string) string {
	t.Helper()
	_, ok := workContextSingleton.seals.(*sealAnswersFake)
	require.True(t, ok, "the shared issuer is not configured with sealAnswersFake")
	// Sealed to the installation the live module authority answers, when the
	// test wires one, so the host's own sealed-versus-live comparison holds the
	// credential to the installation it was minted for.
	installation := "installation-" + principalID
	if service != nil {
		if live, _, err := service.WorkContextSeal(context.Background(), tenant, principalID, ""); err == nil {
			installation = live
		}
	}
	token, _, err := WorkContextSingleton().StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{
		PrincipalID:    principalID,
		Tenant:         tenant,
		InstallationID: installation,
	})
	require.NoError(t, err)
	return token
}

// WorkContextOperationBinding answers from the business service the test wired,
// which owns the delegation records a binding is read from; with none, no
// binding exists.
func (f *sealAnswersFake) WorkContextOperationBinding(ctx context.Context, orgID, bindingID string) (business.WorkContextOperationBinding, error) {
	if service == nil {
		return business.WorkContextOperationBinding{}, business.ErrNoOperationBinding
	}
	return service.WorkContextOperationBinding(ctx, orgID, bindingID)
}
