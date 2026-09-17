package adapters

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	ed25519minter "accounts/pkg/auth/ed25519"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// memoryRevocations is the real revocation semantic the minter's verify path
// consults: a session marker written by RevokeSessionAccess must make every
// access token carrying that sid fail verification.
type memoryRevocations struct {
	mu       sync.Mutex
	jtis     map[string]bool
	sessions map[string]bool
}

func newMemoryRevocations() *memoryRevocations {
	return &memoryRevocations{jtis: map[string]bool{}, sessions: map[string]bool{}}
}

func (r *memoryRevocations) Revoke(_ context.Context, jti string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jtis[jti] = true
	return nil
}

func (r *memoryRevocations) IsRevoked(_ context.Context, jti string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jtis[jti], nil
}

func (r *memoryRevocations) RevokeSession(_ context.Context, sessionID string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sessionID] = true
	return nil
}

func (r *memoryRevocations) IsSessionRevoked(_ context.Context, sessionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID], nil
}

// stopSessionStore records the session rows the minter inserts, which is what
// gives the stop path an elapsed window to report.
type stopSessionStore struct {
	mu      sync.Mutex
	records map[uuid.UUID]auth.SessionRecord
}

func newStopSessionStore() *stopSessionStore {
	return &stopSessionStore{records: map[uuid.UUID]auth.SessionRecord{}}
}

func (s *stopSessionStore) Insert(_ context.Context, rec *auth.SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[rec.ID] = *rec
	return nil
}

func (s *stopSessionStore) FindByRefreshHash(context.Context, []byte) (*auth.SessionRecord, error) {
	return nil, auth.ErrRefreshRevoked
}

func (s *stopSessionStore) RotateRefresh(context.Context, []byte,
	func(*auth.SessionRecord, auth.RefreshAuthorization) (*auth.SessionRecord, error)) error {
	return auth.ErrRefreshRevoked
}

func (s *stopSessionStore) ExchangeOrganization(context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
	func(*auth.SessionRecord, auth.RefreshAuthorization) error) error {
	return nil
}

func (s *stopSessionStore) RevokeFamily(context.Context, uuid.UUID, string) error { return nil }

// close mirrors the production statement: it matches only a row that is still
// open, so a second call reports closed=false instead of moving the close time.
func (s *stopSessionStore) close(sessionID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return time.Time{}, false
	}
	rec, ok := s.records[id]
	if !ok || rec.RevokedAt != nil {
		return time.Time{}, false
	}
	now := time.Now()
	rec.RevokedAt = &now
	s.records[id] = rec
	return rec.IssuedAt, true
}

func (s *stopSessionStore) isOpen(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[uuid.MustParse(sessionID)]
	return ok && rec.RevokedAt == nil
}

// stopImpersonationStore answers the reads the start and stop journeys touch.
// Everything else is the embedded nil interface, so an unexpected read panics
// rather than returning a zero value that looks like an answer.
type stopImpersonationStore struct {
	business.Store
	sessions *stopSessionStore

	platformRoles map[string]string
	userStatus    map[string]gen.UserStatus
	closeErr      error
}

func (f *stopImpersonationStore) GetPlatformRole(_ context.Context, userID string) (string, error) {
	return f.platformRoles[userID], nil
}

func (f *stopImpersonationStore) GetUser(_ context.Context, userID string) (*gen.User, error) {
	status, ok := f.userStatus[userID]
	if !ok {
		status = gen.UserStatus_USER_STATUS_ACTIVE
	}
	return &gen.User{Uuid: userID, Status: status}, nil
}

func (f *stopImpersonationStore) ListOrganizationsForUser(context.Context, string) ([]*gen.Organization, error) {
	return nil, nil
}

func (f *stopImpersonationStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *stopImpersonationStore) WithUserTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *stopImpersonationStore) HasVerifiedMFA(context.Context, string) (bool, error) {
	return false, nil
}

func (f *stopImpersonationStore) CloseImpersonationSession(_ context.Context, sessionID, _ string) (time.Time, bool, error) {
	if f.closeErr != nil {
		return time.Time{}, false, f.closeErr
	}
	startedAt, closed := f.sessions.close(sessionID)
	return startedAt, closed, nil
}

// capturedAudit keeps every entry the journey writes, in order, so a test can
// assert the shape of the trail rather than just that something was recorded.
//
// It also records what the registry makes of each payload. The durable emitter
// only warns on a payload field the event never declared and then drops the
// payload, so an undeclared field costs the trail exactly the detail it was
// added for without failing anything a test would otherwise notice.
type capturedAudit struct {
	mu               sync.Mutex
	entries          []business.AuditEntry
	validationErrors []error
}

func (c *capturedAudit) record(entry business.AuditEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
	if err := business.ValidatePayload(entry.EventType, entry.Payload); err != nil {
		c.validationErrors = append(c.validationErrors, err)
	}
}

func (c *capturedAudit) Emit(_ context.Context, entry business.AuditEntry) {
	c.record(entry)
}

func (c *capturedAudit) EmitTx(_ context.Context, entry business.AuditEntry) error {
	c.record(entry)
	return nil
}

func (c *capturedAudit) errors() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validationErrors
}

func (c *capturedAudit) ofType(eventType business.EventType) []business.AuditEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []business.AuditEntry
	for _, entry := range c.entries {
		if entry.EventType == eventType {
			out = append(out, entry)
		}
	}
	return out
}

// impersonationRig is a real minter over in-memory session and revocation
// stores, wired into the package-level service the RPC handlers use.
type impersonationRig struct {
	minter      *ed25519minter.Minter
	revocations *memoryRevocations
	sessions    *stopSessionStore
	audit       *capturedAudit
	store       *stopImpersonationStore
}

func newImpersonationRig(t *testing.T) *impersonationRig {
	t.Helper()

	sessions := newStopSessionStore()
	_, priv, err := ed25519minter.GenerateKey()
	require.NoError(t, err)
	minter := ed25519minter.New(ed25519minter.Config{
		Issuer:         "stop-impersonation-test",
		Audience:       "stop-impersonation-test",
		AccessTokenTTL: 3 * time.Minute,
	}, priv, sessions)
	revocations := newMemoryRevocations()
	minter.SetRevoker(revocations)

	rig := &impersonationRig{
		minter:      minter,
		revocations: revocations,
		sessions:    sessions,
		audit:       &capturedAudit{},
		store: &stopImpersonationStore{
			sessions:      sessions,
			platformRoles: map[string]string{supportActorID: "support"},
			userStatus:    map[string]gen.UserStatus{},
		},
	}

	previous := service
	svc, err := business.NewService(rig.store)
	require.NoError(t, err)
	svc.SetJWTMinter(minter)
	svc.SetAuditEmitter(rig.audit)
	service = svc
	t.Cleanup(func() { service = previous })

	return rig
}

// start mints an impersonation session the way the console does, and returns
// the access token, the window's sid, and the context a request bearing it
// arrives with. The sid is read here, while the token still verifies, so an
// assertion after the stop does not need to re-parse a revoked token.
func (r *impersonationRig) start(t *testing.T, subjectID string) (string, string, context.Context) {
	t.Helper()

	adminCtx := stampVerifiedIdentity(context.Background(), supportActorID, "", auth.Assurance{
		MFAVerifiedAt: time.Now(),
	})
	resp, err := (&PlatformAdminServer{}).ImpersonateUser(adminCtx,
		&gen.ImpersonateUserRequest{UserId: subjectID, Reason: "investigating a billing discrepancy"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetAccessToken())

	identity, err := r.minter.VerifyAccess(resp.GetAccessToken())
	require.NoError(t, err)
	return resp.GetAccessToken(), identity.SessionID.String(), stampRequestIdentity(
		context.Background(), auth.RequestIdentityOf(identity), auth.Assurance{})
}

// The whole point of the RPC: the bearer itself stops working. Asserting on the
// client's state would pass even against the purely client-side exit this
// replaces.
func TestStopImpersonationInvalidatesTheImpersonationToken(t *testing.T) {
	rig := newImpersonationRig(t)
	token, sessionID, ctx := rig.start(t, targetMemberID)

	_, err := rig.minter.VerifyAccess(token)
	require.NoError(t, err, "the token must be live before the stop")

	resp, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.GetDurationSeconds(), int64(0))

	_, err = rig.minter.VerifyAccess(token)
	require.ErrorIs(t, err, auth.ErrTokenRevoked)
	require.True(t, resp.GetAccessTokenRevoked())
	require.False(t, resp.GetAlreadyClosed())

	// The revocation list is a cache with a TTL; the session row is the record
	// every "is this window open" reader consults, including the window cap and
	// the operator's own session list.
	require.False(t, rig.sessions.isOpen(sessionID),
		"the window must be closed durably, not only in the revocation list")
}

// The revocation list is optional wiring: with no store configured every revoke
// succeeds and revokes nothing. The stop still has to close the window durably,
// and it must not claim a token died that is still live — the audit trail is the
// thing that would be wrong, and it is append-only.
func TestStopImpersonationReportsAnUnrevokedTokenWhenRevocationIsNotWired(t *testing.T) {
	rig := newImpersonationRig(t)
	rig.minter.SetRevoker(auth.NoopTokenRevoker{})
	token, sessionID, ctx := rig.start(t, targetMemberID)

	resp, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)
	require.False(t, resp.GetAccessTokenRevoked(), "no revocation store means no token was killed")

	_, err = rig.minter.VerifyAccess(token)
	require.NoError(t, err, "without a revocation list the token necessarily outlives the stop")

	require.False(t, rig.sessions.isOpen(sessionID),
		"the durable close does not depend on the revocation list")

	ended := rig.audit.ofType(business.EventPlatformImpersonationEnded)
	require.Len(t, ended, 1)
	require.Equal(t, false, ended[0].Payload["access_token_revoked"],
		"the record must not assert a kill that did not happen")
	require.Empty(t, rig.audit.errors())
}

// A retry, or a second tab, must not record a second end for one window.
func TestStopImpersonationIsIdempotent(t *testing.T) {
	rig := newImpersonationRig(t)
	_, _, ctx := rig.start(t, targetMemberID)

	first, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)
	require.False(t, first.GetAlreadyClosed())

	second, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err, "a repeated stop is not an error")
	require.True(t, second.GetAlreadyClosed())

	require.Len(t, rig.audit.ofType(business.EventPlatformImpersonationEnded), 1,
		"the trail must not show more ends than starts")
}

// The durable close is the operation; if it cannot commit, nothing is recorded
// and the caller is told so rather than being handed a success whose record
// never landed.
func TestStopImpersonationFailsWhenTheWindowCannotBeClosed(t *testing.T) {
	rig := newImpersonationRig(t)
	_, _, ctx := rig.start(t, targetMemberID)
	rig.store.closeErr = context.DeadlineExceeded

	_, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.Error(t, err)
	require.Empty(t, rig.audit.ofType(business.EventPlatformImpersonationEnded))
}

// The admin steps back into their own session, so their own credentials must
// survive the stop untouched — only the impersonation sid is marked.
func TestStopImpersonationLeavesTheAdminSessionIntact(t *testing.T) {
	rig := newImpersonationRig(t)
	_, _, ctx := rig.start(t, targetMemberID)

	adminPair, err := rig.minter.Mint(context.Background(), &auth.Identity{
		UserID:       uuid.MustParse(supportActorID),
		PlatformRole: "support",
	})
	require.NoError(t, err)

	_, err = (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)

	adminIdentity, err := rig.minter.VerifyAccess(adminPair.AccessToken)
	require.NoError(t, err, "the admin's own access token must still verify")
	require.Equal(t, "support", adminIdentity.PlatformRole)

	adminCtx := stampVerifiedIdentity(context.Background(), supportActorID, "", auth.Assurance{})
	require.NoError(t, requirePlatformRole(adminCtx, supportActorID, "support"),
		"the admin's platform authority must be intact")
}

// The end event carries both principals the way every other impersonated action
// does: the effective subject is the actor of record, and the admin behind it is
// named separately.
func TestStopImpersonationRecordsBothPrincipals(t *testing.T) {
	rig := newImpersonationRig(t)
	_, _, ctx := rig.start(t, targetMemberID)

	_, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)

	ended := rig.audit.ofType(business.EventPlatformImpersonationEnded)
	require.Len(t, ended, 1)
	require.Equal(t, targetMemberID, ended[0].ActorID)
	require.True(t, ended[0].IsImpersonated)
	require.Equal(t, supportActorID, ended[0].ImpersonatedBy)
	require.Equal(t, targetMemberID, ended[0].ResourceID)

	started := rig.audit.ofType(business.EventPlatformImpersonated)
	require.Len(t, started, 1)
	require.Equal(t, started[0].Payload["session_id"], ended[0].Payload["session_id"],
		"the two sides of one window must name the same session")
	require.Contains(t, ended[0].Payload, "duration_seconds")
	require.Empty(t, rig.audit.errors(),
		"both events must declare the payload fields they carry")
}

// Two support windows onto the same target must reconcile as two closed windows,
// not as an interleaved pair a reader has to guess the pairing of.
func TestRepeatedImpersonationProducesTwoClosedWindows(t *testing.T) {
	rig := newImpersonationRig(t)

	_, _, first := rig.start(t, targetMemberID)
	_, err := (&PlatformAdminServer{}).StopImpersonation(first, &gen.StopImpersonationRequest{})
	require.NoError(t, err)

	_, _, second := rig.start(t, targetMemberID)
	_, err = (&PlatformAdminServer{}).StopImpersonation(second, &gen.StopImpersonationRequest{})
	require.NoError(t, err)

	started := rig.audit.ofType(business.EventPlatformImpersonated)
	ended := rig.audit.ofType(business.EventPlatformImpersonationEnded)
	require.Len(t, started, 2)
	require.Len(t, ended, 2)

	firstSession := started[0].Payload["session_id"]
	secondSession := started[1].Payload["session_id"]
	require.NotEqual(t, firstSession, secondSession)
	require.Equal(t, firstSession, ended[0].Payload["session_id"])
	require.Equal(t, secondSession, ended[1].Payload["session_id"])
}

// Being impersonated is the authorization. An ordinary session — including a
// platform admin's own — has no window to close and is denied.
func TestStopImpersonationDeniesAnUnimpersonatedCaller(t *testing.T) {
	rig := newImpersonationRig(t)

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"platform admin on their own session",
			stampVerifiedIdentity(context.Background(), supportActorID, "", auth.Assurance{})},
		{"ordinary user", stampVerifiedIdentity(context.Background(), targetMemberID, targetOrgID, auth.Assurance{})},
		{"unauthenticated", context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&PlatformAdminServer{}).StopImpersonation(tc.ctx, &gen.StopImpersonationRequest{})
			require.Error(t, err)
			require.Contains(t, []codes.Code{codes.PermissionDenied, codes.Unauthenticated}, status.Code(err))
			require.Empty(t, rig.audit.ofType(business.EventPlatformImpersonationEnded))
		})
	}
}

// One operator must not be able to end another's window. The guarantee is
// structural rather than a validation rule: the request has no field to name a
// session with, so there is nothing to forge.
func TestStopImpersonationRequestCarriesNoTarget(t *testing.T) {
	fields := (&gen.StopImpersonationRequest{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, 0, fields.Len(), "a target field would reintroduce caller-chosen session selection")
}

// A revocation store that is reachable but failing must not block the way out:
// the window still closes durably, and the record says the token was not killed
// rather than claiming a kill that did not happen. Failing the whole stop here
// would leave the operator inside the target's session over a cache error.
func TestStopImpersonationClosesTheWindowWhenTheRevocationWriteFails(t *testing.T) {
	rig := newImpersonationRig(t)
	token, sessionID, ctx := rig.start(t, targetMemberID)

	rig.minter.SetRevoker(failingRevoker{})

	resp, err := (&PlatformAdminServer{}).StopImpersonation(ctx, &gen.StopImpersonationRequest{})
	require.NoError(t, err)
	require.False(t, resp.GetAccessTokenRevoked())

	require.False(t, rig.sessions.isOpen(sessionID),
		"a failed marker must not leave the window open in the durable record")

	ended := rig.audit.ofType(business.EventPlatformImpersonationEnded)
	require.Len(t, ended, 1)
	require.Equal(t, false, ended[0].Payload["access_token_revoked"])

	// The token does outlive the stop — that is the fact the record now carries.
	rig.minter.SetRevoker(rig.revocations)
	_, err = rig.minter.VerifyAccess(token)
	require.NoError(t, err)
}

type failingRevoker struct{ auth.NoopTokenRevoker }

func (failingRevoker) RevokeSession(context.Context, string, time.Duration) error {
	return context.DeadlineExceeded
}
