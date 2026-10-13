//go:build !race_only

package business_test

// DelegateSourceTriggers records the reference a declared release's triggers
// run under, for a source that is ALREADY connected. These tests hold it to
// being a backfill and nothing more (issue #1112, M2).
//
// It used to run the connect path, which is correct for a connect and
// destructive for a backfill: it revoked every delegation on the source as
// `replaced` and re-inserted each one owned by the calling administrator. The
// two tests below are the two harms that caused, and they are written against
// the delegation rows rather than against the method's own return value — a
// method agreeing with itself is what let this ship.

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// Three modules, each with one binding that accepts a delegation, and they are
// not interchangeable.
//
// `docstore` and `reportstore` are CONSUMING modules: their bindings name
// another service as the audience, which is what a person's connect delegates
// to. `runtime` is the module that EXECUTES declared work: its binding names
// its own prefix, because it mints the capability for an occurrence and then
// admits that capability as its own incoming execution context.
//
// The distinction is what the backfill is narrowed to (issue #1121, N8): a
// trigger's reference arm is a source id, a mint by source id resolves the
// delegation of the CALLING module, and the capability is addressed to the
// binding's audience — so only the self-admitting shape can serve one. A
// delegation recorded for a consuming module is the administrator's authority
// handed to a module whose delegation no trigger of this host could name.
const delegateTriggerPrincipals = `{` +
	`"docstore":{"workload":{"service_account":"docstore","namespace":"acme-prod","container":"app"},` +
	`"tenant":"` + testOrg + `","operation_audiences":{"source-sync":{"audience":"ingestservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"reportstore":{"workload":{"service_account":"reportstore","namespace":"acme-prod","container":"app"},` +
	`"tenant":"` + testOrg + `","operation_audiences":{"source-sync":{"audience":"reportservice",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}},` +
	`"runtime":{"workload":{"service_account":"runtime","namespace":"acme-prod","container":"app"},` +
	`"tenant":"` + testOrg + `","operation_audiences":{"occurrences":{"audience":"runtime",` +
	`"invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],` +
	`"headless_scopes":[{"resource_kind":"collections","actions":["read"]}],` +
	`"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}}}`

// A delegation another module already holds survives an administrator's
// reference operation, owner and row id intact.
//
// What this refuses is the state the connect path produced: `docstore`'s
// delegation revoked as `replaced` and re-inserted under the administrator. On
// a shared source that fails every in-flight child's re-check and runs
// everything afterwards under the administrator's authority instead of the
// connector's — a silent change of whose authority a module is acting with.
func TestDelegateSourceTriggersLeavesAnotherModulesDelegationAlone(t *testing.T) {
	store := newDelegateTriggerStore(t)
	svc, _ := newDatasourceService(store, &recordingProducer{}, &fakeGitHub{defaultBranch: "main", commit: "abc"})
	// The source is connected BEFORE either module declares a binding that
	// accepts a delegation — which is the only situation this method exists
	// for: a release installed afterwards finds no reference to name, and
	// nothing backfills one.
	source := githubSource(t, svc, "main", nil, "")
	registry, err := moduleregistrytest.Parse(delegateTriggerPrincipals)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, registry)
	// One module's delegation is then recorded by the member who connected the
	// source. The administrator below is a different person entirely.
	held := store.insert(source.ID, "connecting-member", "docstore", registry)

	_, err = svc.DelegateSourceTriggers(context.Background(), "an-administrator", testOrg, source.ID)
	require.NoError(t, err)

	after := store.get(held.ID)
	require.NotNil(t, after, "the delegation another module held was deleted")
	require.True(t, after.Active(), "an administrator's reference operation revoked another module's delegation")
	require.Equal(t, "connecting-member", after.PrincipalID,
		"an administrator's reference operation re-owned another module's delegation")

	// And the EXECUTING module, which had none, got one — that IS the point of
	// the call: the reference a declared release's trigger names.
	recorded := store.activeFor(source.ID, "runtime")
	require.NotNil(t, recorded, "the executing module with no delegation did not get one")
	require.Equal(t, "an-administrator", recorded.PrincipalID)

	// The other CONSUMING module did not (issue #1121, N8). It accepts
	// delegations, and it had none, and the old backfill recorded one anyway —
	// handing the administrator's authority over this source to a module whose
	// delegation no trigger of this host can name. A person's connect is what
	// delegates to a consuming module, and that path is unchanged.
	require.Nil(t, store.activeFor(source.ID, "reportstore"),
		"the backfill recorded a reference for a module that executes no declared work")

	// No replacement anywhere.
	require.Equal(t, 0, store.revocations, "a backfill revoked something")
}

// A delegation the HOLDER's own current declaration no longer supports is not
// "nothing was missing" (issue #1121, N8). Holder, not executor: a
// self-addressed target is one that COULD serve a trigger, never one proved to
// fire the release (F1).
//
// A delegation carries a digest of its binding's audience and delegation
// scopes, and the very next mint revokes one whose binding has changed
// (`binding_changed`). The backfill used to skip any target holding an active
// row, so an operator pressing the button over a source whose self-addressed
// holder's binding had moved was told nothing was missing — over a trigger that
// could never mint. It is revoked here, under the same reason the mint would use, and
// a fresh one is recorded in the same transaction.
func TestDelegateSourceTriggersReplacesTheSelfAddressedHoldersStaleDelegation(t *testing.T) {
	store := newDelegateTriggerStore(t)
	svc, _ := newDatasourceService(store, &recordingProducer{}, &fakeGitHub{defaultBranch: "main", commit: "abc"})
	source := githubSource(t, svc, "main", nil, "")
	registry, err := moduleregistrytest.Parse(delegateTriggerPrincipals)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, registry)

	// The holder's delegation as recorded against an EARLIER shape of its
	// binding: same binding id, a digest that no longer matches.
	stale := store.insert(source.ID, "connecting-member", "runtime", registry)
	stale.BindingDigest = "0" + stale.BindingDigest[1:]

	_, err = svc.DelegateSourceTriggers(context.Background(), "an-administrator", testOrg, source.ID)
	require.NoError(t, err)

	gone := store.get(stale.ID)
	require.NotNil(t, gone)
	require.False(t, gone.Active(), "a delegation the next mint would revoke was left in place")
	require.Equal(t, business.SourceDelegationBindingChanged, gone.RevokedReason,
		"it is revoked for the reason the mint itself would record")

	fresh := store.activeFor(source.ID, "runtime")
	require.NotNil(t, fresh, "nothing replaced the unusable delegation")
	require.NotEqual(t, stale.ID, fresh.ID)
	require.Equal(t, "an-administrator", fresh.PrincipalID)

	// And a delegation that IS current is still left exactly as it is: the
	// revocation above is not the connect path creeping back.
	store.revocations = 0
	_, err = svc.DelegateSourceTriggers(context.Background(), "another-administrator", testOrg, source.ID)
	require.NoError(t, err)
	require.Equal(t, 0, store.revocations, "a current delegation was replaced")
	require.Equal(t, fresh.ID, store.activeFor(source.ID, "runtime").ID)
}

// A personal source is its owner's. An administrator is refused rather than
// recording a reference whose authority is theirs.
//
// A reference on a source makes occurrences borrow the authority of the person
// the delegation names. On a personal source that person must be its owner:
// ReconcileDatasourceSource refuses a personal source to anyone but its owner,
// so recording a reference under an administrator would bind a trigger to an
// authority that cannot perform the pass it admits. (The role gate before it,
// ReconcileSourceAuthority, admits the owner OR an administrator — so it is
// not what draws this line, and a comment saying it was would send a reader
// to the wrong function.)
func TestDelegateSourceTriggersRefusesAnotherMembersPersonalSource(t *testing.T) {
	store := newDelegateTriggerStore(t)
	svc, _ := newDatasourceService(store, &recordingProducer{}, &fakeGitHub{defaultBranch: "main", commit: "abc"})
	source := githubSource(t, svc, "main", nil, "")
	registry, err := moduleregistrytest.Parse(delegateTriggerPrincipals)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, registry)
	store.setPersonalOwner(t, source.ID, "the-owner")

	_, err = svc.DelegateSourceTriggers(context.Background(), "an-administrator", testOrg, source.ID)
	require.ErrorIs(t, err, business.ErrTriggerSourcePersonal)
	require.Empty(t, store.delegations, "a refusal recorded a delegation")

	// The owner is not refused: it is their source, and the authority a
	// reference borrows is exactly theirs.
	_, err = svc.DelegateSourceTriggers(context.Background(), "the-owner", testOrg, source.ID)
	require.NoError(t, err)
	// One row: the executing module's. The two consuming modules get nothing
	// from a backfill (issue #1121, N8).
	require.Len(t, store.delegations, 1, "the owner's own reference operation recorded nothing")
	require.NotNil(t, store.activeFor(source.ID, "runtime"))
}

// delegateTriggerStore is the datasource fake plus the delegation rows and the
// connect-role read DelegateSourceTriggers needs.
type delegateTriggerStore struct {
	*datasourceFakeStore
	delegations map[string]*business.SourceDelegation
	revocations int
}

func newDelegateTriggerStore(t *testing.T) *delegateTriggerStore {
	t.Helper()
	return &delegateTriggerStore{
		datasourceFakeStore: newDatasourceFakeStore(),
		delegations:         map[string]*business.SourceDelegation{},
	}
}

// Both actors in these tests hold the connect role; the role is not what is
// under test and a member who lacked it would record nothing at all.
func (s *delegateTriggerStore) SourceDelegationMemberRole(context.Context, string, string) (string, error) {
	return "admin", nil
}

func (s *delegateTriggerStore) ActiveSourceDelegation(_ context.Context, sourceID, prefix string) (*business.SourceDelegation, error) {
	return s.activeFor(sourceID, prefix), nil
}

func (s *delegateTriggerStore) InsertSourceDelegation(_ context.Context, d *business.SourceDelegation) error {
	copied := *d
	copied.CreatedAt = time.Now()
	s.delegations[d.ID] = &copied
	return nil
}

// Counted, not just performed: "it revoked nothing" is the assertion, and a
// revoke that returned no rows would otherwise look identical to one never
// called.
func (s *delegateTriggerStore) RevokeSourceDelegations(
	_ context.Context, filter business.SourceDelegationFilter, reason, by string,
) ([]*business.SourceDelegation, error) {
	s.revocations++
	var out []*business.SourceDelegation
	for _, d := range s.delegations {
		if d.OrgID != filter.OrgID || !d.Active() {
			continue
		}
		if filter.SourceID != "" && d.SourceID != filter.SourceID {
			continue
		}
		if filter.ModulePrefix != "" && d.ModulePrefix != filter.ModulePrefix {
			continue
		}
		now := time.Now()
		d.RevokedAt, d.RevokedReason, d.RevokedBy = &now, reason, by
		copied := *d
		out = append(out, &copied)
	}
	return out, nil
}

func (s *delegateTriggerStore) ListSourceDelegations(_ context.Context, orgID, sourceID string, includeRevoked bool) ([]*business.SourceDelegation, error) {
	var out []*business.SourceDelegation
	for _, d := range s.delegations {
		if d.OrgID != orgID || sourceID != "" && d.SourceID != sourceID {
			continue
		}
		if !includeRevoked && !d.Active() {
			continue
		}
		copied := *d
		out = append(out, &copied)
	}
	return out, nil
}

func (s *delegateTriggerStore) insert(sourceID, principal, prefix string, registry business.ModulePrincipalRegistry) *business.SourceDelegation {
	binding := registry[business.ModulePrincipalID(prefix)].OperationAudiences["source-sync"]
	delegation := &business.SourceDelegation{
		ID: business.NewIDString(), OrgID: testOrg, SourceID: sourceID, PrincipalID: principal,
		ModulePrefix: prefix, BindingID: "source-sync",
		BindingDigest: business.SourceDelegationBindingDigest(binding), CreatedAt: time.Now(),
	}
	s.delegations[delegation.ID] = delegation
	return delegation
}

func (s *delegateTriggerStore) get(id string) *business.SourceDelegation { return s.delegations[id] }

func (s *delegateTriggerStore) activeFor(sourceID, prefix string) *business.SourceDelegation {
	for _, d := range s.delegations {
		if d.SourceID == sourceID && d.ModulePrefix == prefix && d.Active() {
			return d
		}
	}
	return nil
}

func (s *delegateTriggerStore) setPersonalOwner(t *testing.T, sourceID, owner string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.sources[sourceID]
	require.True(t, ok, "source %q not found", sourceID)
	source.PersonalOwnerUserID = owner
}

var _ = gen.OrgRole_ORG_ROLE_ADMIN
