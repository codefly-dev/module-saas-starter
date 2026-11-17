package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The hop in a cell is the gateway, which keeps no journal: it tells accounts
// each call its front door refused itself, with the internal credential, and
// accounts records it. Nobody else is believed: what a hop refused is whatever
// the caller says it is.
func TestTheGatewaysHopRefusalIsRecordedInTheJournal(t *testing.T) {
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous; SetInternalToken("") })
	audit := &recordingAuditEmitter{}
	svc.SetAuditEmitter(audit)
	SetInternalToken("hop-refusal-test")
	gateway := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "hop-refusal-test"))
	server := &WorkContextAuthorityServer{}
	org, owner := uuid.NewString(), uuid.NewString()
	refusal := &gen.RecordHopRefusalRequest{Method: "/records.Service/Read", Code: "Unavailable", Reason: "the module did not answer",
		Module: "records", OrganizationId: org, OwnerPrincipalId: owner, ActorPrincipalId: owner, BindingId: "records", TaskId: "task-1"}

	// No internal credential: refused before anything is read or written.
	_, err = server.RecordHopRefusal(context.Background(), refusal)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = server.RecordHopRefusal(metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "another")), refusal)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, audit.entries)

	_, err = server.RecordHopRefusal(gateway, refusal)
	require.NoError(t, err)
	entry := audit.only(t)
	require.Equal(t, business.EventAuthorityDecisionRefused, entry.EventType)
	require.Equal(t, org, entry.OrgID)
	require.Equal(t, owner, entry.ActorID)
	require.Equal(t, map[string]any{"decision": "hop", "code": "Unavailable", "reason": "the module did not answer", "method": "/records.Service/Read",
		"audience": "records", "owner": owner, "actor": owner, "binding": "records", "task": "task-1"}, entry.Payload)

	// What an unverified Work Context claims is filed under no organization.
	audit.entries = nil
	claimed := &gen.RecordHopRefusalRequest{Method: "POST /v1/records", Code: "DeadlineExceeded", Reason: "the authority did not answer in time",
		OrganizationId: org, OwnerPrincipalId: owner, ActorPrincipalId: owner, Claimed: true}
	_, err = server.RecordHopRefusal(gateway, claimed)
	require.NoError(t, err)
	entry = audit.only(t)
	require.Empty(t, entry.OrgID)
	require.Empty(t, entry.ActorID)
	require.Equal(t, true, entry.Payload["claimed"])

	// A refusal that names no call is not one.
	_, err = server.RecordHopRefusal(gateway, &gen.RecordHopRefusalRequest{Code: "Unavailable"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
