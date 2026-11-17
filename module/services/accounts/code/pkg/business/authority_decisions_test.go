package business

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// decisionJournal is the two halves of the host's audit journal a decision is
// written through: the transaction a row is filed in (an organization's, or
// the control plane's for a row filed under no tenant) and the emitter that
// writes it. Either can be made to fail.
type decisionJournal struct {
	Store
	core    *memstore.Store
	entries []AuditEntry
	// filedIn is, per entry, the organization whose transaction it was
	// written in; "" is the control plane's.
	filedIn []string
	open    string
	fail    error
}

func (j *decisionJournal) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	j.open = org
	return fn(ctx)
}

func (j *decisionJournal) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	j.open = ""
	return fn(ctx)
}

func (j *decisionJournal) AuthorityCoreWithConsent(ModuleConsent) authority.Store { return j.core }
func (j *decisionJournal) AuthorityCoreReplay() workcontext.ReplayStore {
	return workcontext.NewMemoryReplayStore()
}

// Emit is the emitter's own transaction, which it opens in the entry's
// organization, or in the control plane for one that names none.
func (j *decisionJournal) Emit(ctx context.Context, entry AuditEntry) {
	j.open = entry.OrgID
	_ = j.EmitTx(ctx, entry)
}

func (j *decisionJournal) EmitTx(_ context.Context, entry AuditEntry) error {
	if j.fail != nil {
		return j.fail
	}
	j.entries = append(j.entries, entry)
	j.filedIn = append(j.filedIn, j.open)
	return nil
}

func decisionService(t *testing.T) (*Service, *decisionJournal) {
	t.Helper()
	journal := &decisionJournal{core: memstore.New()}
	s := &Service{store: journal}
	s.SetAuditEmitter(journal)
	return s, journal
}

const (
	decisionOrg    = "00000000-0000-7000-8000-0000000000a1"
	decisionPerson = "00000000-0000-7000-8000-0000000000b1"
)

// Owner decision 11: the authority's refusals and issues are rows of the
// journal this host already keeps. A refusal is filed in the organization it
// was decided in, as the person who asked, with what was asked and why it was
// refused, which the error the caller received does not say.
func TestARefusedDecisionIsARowOfItsOrganizationsJournal(t *testing.T) {
	s, journal := decisionService(t)
	require.NoError(t, s.recordAuthorityDecision(context.Background(), authority.Decision{
		Kind: authority.DecisionIssuePerson, Outcome: authority.Refused, Code: authority.CodeDenied,
		Reason:       decisionPerson + " holds no role on pages",
		Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Audience: "pages", Binding: "records", Operation: "invoke",
	}))
	require.Len(t, journal.entries, 1)
	entry := journal.entries[0]
	require.Equal(t, EventAuthorityDecisionRefused, entry.EventType)
	require.Equal(t, decisionOrg, entry.OrgID)
	require.Equal(t, decisionOrg, journal.filedIn[0], "written in the organization's own transaction")
	require.Equal(t, decisionPerson, entry.ActorID)
	require.Equal(t, ActorTypeUser, entry.ActorType)
	require.Equal(t, map[string]any{
		"decision": "issue.person", "code": "denied", "reason": decisionPerson + " holds no role on pages",
		"owner": decisionPerson, "actor": decisionPerson, "audience": "pages", "binding": "records", "operation": "invoke",
	}, entry.Payload)
	require.NoError(t, ValidatePayload(entry.EventType, entry.Payload))

	// A refused check is a refusal like any other, with the method it named;
	// a module's workload is recorded as the system, its name in the payload.
	require.NoError(t, s.recordAuthorityDecision(context.Background(), authority.Decision{
		Kind: authority.DecisionCheck, Outcome: authority.Refused, Code: authority.CodeDenied, Reason: "records requires example:write",
		Organization: decisionOrg, Owner: decisionPerson, Actor: "pages", Audience: "records", Method: "/records.Service/Write",
		Installation: "inst-1", Task: "task-1",
	}))
	require.Len(t, journal.entries, 2)
	entry = journal.entries[1]
	require.Equal(t, EventAuthorityDecisionRefused, entry.EventType)
	require.Equal(t, decisionOrg, entry.OrgID)
	require.Empty(t, entry.ActorID)
	require.Equal(t, ActorTypeSystem, entry.ActorType)
	require.Equal(t, "pages", entry.Payload["actor"])
	require.Equal(t, "/records.Service/Write", entry.Payload["method"])
	require.Equal(t, "task-1", entry.Payload["task"])
	require.NoError(t, ValidatePayload(entry.EventType, entry.Payload))
}

// One admitted check is made per served call, and a resource listing beside
// it: neither is recorded.
func TestAnAdmittedCheckIsNotRecorded(t *testing.T) {
	s, journal := decisionService(t)
	journal.fail = errors.New("the journal is down")
	for _, kind := range []authority.DecisionKind{authority.DecisionCheck, authority.DecisionListResources} {
		require.NoError(t, s.recordAuthorityDecision(context.Background(), authority.Decision{
			Kind: kind, Outcome: authority.Admitted, Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Audience: "records",
		}), "an admitted %s asks nothing of the journal", kind)
	}
	require.Empty(t, journal.entries)
}

// What a capability that did not verify says of itself is proven by nothing:
// the row is kept, under no tenant, and the organization it claims is named
// as a claim. It is never filed in that organization's journal on its word.
func TestARefusalOfAnUnverifiedCapabilityIsFiledUnderNoTenant(t *testing.T) {
	s, journal := decisionService(t)
	require.NoError(t, s.recordAuthorityDecision(context.Background(), authority.Decision{
		Kind: authority.DecisionCheck, Outcome: authority.Refused, Code: authority.CodeUnauthenticated, Reason: "the capability expired",
		Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Audience: "records", Task: "task-1", Claimed: true,
	}))
	require.Len(t, journal.entries, 1)
	entry := journal.entries[0]
	require.Empty(t, entry.OrgID, "filed under no tenant")
	require.Empty(t, journal.filedIn[0], "and written in no organization's transaction")
	require.Empty(t, entry.ActorID, "nobody is proven to have asked")
	require.Equal(t, ActorTypeSystem, entry.ActorType)
	require.Equal(t, true, entry.Payload["claimed"])
	require.Equal(t, decisionOrg, entry.Payload["organization"])
	require.Equal(t, decisionPerson, entry.Payload["actor"])
	require.NoError(t, ValidatePayload(entry.EventType, entry.Payload))

	// So is a refusal that names no organization this host could file it in.
	require.NoError(t, s.recordAuthorityDecision(context.Background(), authority.Decision{
		Kind: authority.DecisionIssueWorkload, Outcome: authority.Refused, Code: authority.CodeNotFound, Reason: "no such module", Actor: "pages", Audience: "nowhere",
	}))
	require.Len(t, journal.entries, 2)
	require.Empty(t, journal.entries[1].OrgID)
	require.NotContains(t, journal.entries[1].Payload, "claimed")
}

// An admitted issue is the record it always was: a task started in the
// organization it was issued in, a child session for one more hop. It is
// written before the capability is handed out, and an issue that cannot be
// recorded hands out nothing.
func TestAnAdmittedIssueIsRecordedOrNotHandedOut(t *testing.T) {
	s, journal := decisionService(t)
	issue := authority.Decision{Kind: authority.DecisionIssuePerson, Outcome: authority.Admitted,
		Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Audience: "pages", Installation: "inst-1", Task: "task-1"}
	require.NoError(t, s.recordAuthorityDecision(context.Background(), issue))
	hop := issue
	hop.Kind, hop.Actor, hop.Task = authority.DecisionIssueChild, "pages", "task-2"
	require.NoError(t, s.recordAuthorityDecision(context.Background(), hop))
	// A page's call through its target's binding is that target's hop.
	page := hop
	page.Kind, page.Task = authority.DecisionIssuePage, "task-3"
	require.NoError(t, s.recordAuthorityDecision(context.Background(), page))
	require.Len(t, journal.entries, 3)
	for i, want := range []struct {
		event EventType
		actor string
		task  string
	}{{EventWorkContextTaskStarted, decisionPerson, "task-1"}, {EventWorkContextChildSession, "", "task-2"}, {EventWorkContextChildSession, "", "task-3"}} {
		entry := journal.entries[i]
		require.Equal(t, want.event, entry.EventType)
		require.Equal(t, decisionOrg, entry.OrgID)
		require.Equal(t, decisionOrg, journal.filedIn[i])
		require.Equal(t, want.actor, entry.ActorID)
		require.Equal(t, "work_context", entry.Resource)
		require.Equal(t, want.task, entry.ResourceID)
	}

	journal.fail = errors.New("the journal is down")
	err := s.recordAuthorityDecision(context.Background(), issue)
	require.ErrorIs(t, err, ErrAuthorityDecisionUnrecorded)
	require.NotContains(t, err.Error(), "the journal is down", "the journal's own failure is logged, never answered")
	// An issue in an organization this host has no journal for is not
	// recorded, so it is not handed out either.
	journal.fail = nil
	stray := issue
	stray.Organization = "acme"
	require.ErrorIs(t, s.recordAuthorityDecision(context.Background(), stray), ErrAuthorityDecisionUnrecorded)
	require.Len(t, journal.entries, 3)
}

// What a probe is answered is discarded by the operation that asked (a mint
// held to what the core would issue, a source's governing scope): it is no
// issue and no refusal of its own, and is not recorded as one.
func TestAProbeIsNoDecisionOfItsOwn(t *testing.T) {
	s, journal := decisionService(t)
	probe := AuthorityProbe(context.Background())
	for _, outcome := range []authority.Outcome{authority.Admitted, authority.Refused} {
		require.NoError(t, s.recordAuthorityDecision(probe, authority.Decision{Kind: authority.DecisionIssuePerson, Outcome: outcome,
			Code: authority.CodeDenied, Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Audience: "pages", Task: "task-1"}))
	}
	require.Empty(t, journal.entries)
}

// The core this host builds is told to record: a refused ask made of it is a
// row of the journal with no further wiring by its caller.
func TestTheHostsCoreRecordsItsRefusals(t *testing.T) {
	s, journal := decisionService(t)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := s.newAuthorityCore("accounts.test", "decisions-test", key)
	require.NoError(t, err)
	_, err = core.IssueForPerson(context.Background(), authority.Person{ID: decisionPerson, Organization: decisionOrg},
		authority.IssueRequest{Audience: "nowhere"})
	require.Error(t, err)
	require.Len(t, journal.entries, 1)
	entry := journal.entries[0]
	require.Equal(t, EventAuthorityDecisionRefused, entry.EventType)
	require.Equal(t, decisionOrg, entry.OrgID)
	require.Equal(t, decisionPerson, entry.ActorID)
	require.Equal(t, "issue.person", entry.Payload["decision"])
	require.Equal(t, "not_found", entry.Payload["code"])
	require.Equal(t, "nowhere", entry.Payload["audience"])
	require.Equal(t, err.Error(), entry.Payload["reason"])
}

// The hop's own refusals never reach the core: the hop hands each to the host
// that keeps the journal. It is the same row a refusal of the core's is, with
// the decision named as the hop's. One whose identity the authority checked
// is filed in its organization; one read from a Work Context nobody verified,
// or carrying none, is filed under no tenant.
func TestAHopsOwnRefusalIsARowOfTheJournal(t *testing.T) {
	s, journal := decisionService(t)
	ctx := context.Background()

	// A call that carried no Work Context: nothing says whose it was.
	s.RecordHopRefusal(ctx, HopRefusal{Method: "/records.Service/Read", Code: "Unauthenticated", Reason: "the call carries no Work Context"})
	// A call whose identity the authority checked, refused by the hop after.
	s.RecordHopRefusal(ctx, HopRefusal{Method: "/records.Service/Read", Code: "Unavailable", Reason: "dial tcp 10.0.0.9:8080: connection refused",
		Module: "records", Organization: decisionOrg, Owner: decisionPerson, Actor: "pages", Binding: "records", Task: "task-1"})
	// A call whose Work Context the authority never checked.
	s.RecordHopRefusal(ctx, HopRefusal{Method: "POST /v1/records", Code: "DeadlineExceeded", Reason: "the authority did not answer in time",
		Module: "records", Organization: decisionOrg, Owner: decisionPerson, Actor: decisionPerson, Task: "task-2", Claimed: true})

	require.Len(t, journal.entries, 3)
	for _, entry := range journal.entries {
		require.Equal(t, EventAuthorityDecisionRefused, entry.EventType)
		require.Equal(t, "hop", entry.Payload["decision"])
		require.NoError(t, ValidatePayload(entry.EventType, entry.Payload))
	}
	bare, checked, claimed := journal.entries[0], journal.entries[1], journal.entries[2]
	require.Empty(t, bare.OrgID)
	require.Equal(t, map[string]any{"decision": "hop", "code": "Unauthenticated", "reason": "the call carries no Work Context", "method": "/records.Service/Read"}, bare.Payload)

	require.Equal(t, decisionOrg, checked.OrgID, "the authority checked whose call it was")
	require.Empty(t, checked.ActorID, "a module acting for the owner is recorded as the system, named in the payload")
	require.Equal(t, ActorTypeSystem, checked.ActorType)
	require.Equal(t, "records", checked.Payload["audience"])
	require.Equal(t, "pages", checked.Payload["actor"])
	require.Equal(t, decisionPerson, checked.Payload["owner"])
	require.Equal(t, "records", checked.Payload["binding"])
	require.Equal(t, "task-1", checked.Payload["task"])
	require.Equal(t, "dial tcp 10.0.0.9:8080: connection refused", checked.Payload["reason"], "the hop's own reason, which no caller was told")

	require.Empty(t, claimed.OrgID, "an unverified Work Context files nothing in the organization it names")
	require.Empty(t, claimed.ActorID)
	require.Equal(t, true, claimed.Payload["claimed"])
	require.Equal(t, decisionOrg, claimed.Payload["organization"])
}
