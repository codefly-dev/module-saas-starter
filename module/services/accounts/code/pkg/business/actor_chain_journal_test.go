package business_test

import (
	"encoding/json"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func baseHop() business.ActorChainHopInput {
	return business.ActorChainHopInput{
		ID:               "hop-1",
		OrgID:            "org-1",
		TaskID:           "task-1",
		SessionID:        "session-1",
		OwnerPrincipalID: "owner-1",
		ActorPrincipalID: "agent-1",
		ActorKind:        "agent",
		GrantedScopes: []business.ActorChainScope{
			{ResourceKind: "repo", Actions: []string{"read", "write"}, ResourceIDs: []string{"r2", "r1"}},
		},
		AuthorizationRevision: 7,
		HopIndex:              0,
	}
}

func TestHopContentHash_DeterministicAndScopeOrderIndependent(t *testing.T) {
	hop := baseHop()

	// Same content, differently-ordered actions/resource ids → same address.
	reordered := baseHop()
	reordered.GrantedScopes = []business.ActorChainScope{
		{ResourceKind: "repo", Actions: []string{"write", "read"}, ResourceIDs: []string{"r1", "r2"}},
	}

	require.Equal(t,
		business.HopContentHash(hop, "prev"),
		business.HopContentHash(reordered, "prev"),
		"scope ordering must not change the content address",
	)
}

func TestHopContentHash_ExcludesAssignedMetadata(t *testing.T) {
	hop := baseHop()
	withMetadata := baseHop()
	withMetadata.ID = "different-id"
	withMetadata.ParentDelegationID = "some-parent"
	withMetadata.DelegationGrantID = "some-grant"

	require.Equal(t,
		business.HopContentHash(hop, "prev"),
		business.HopContentHash(withMetadata, "prev"),
		"id, parent, and grant link are metadata, not content",
	)
}

func TestHopContentHash_SensitiveToContentAndChain(t *testing.T) {
	hop := baseHop()
	base := business.HopContentHash(hop, "prev")

	widenedScope := baseHop()
	widenedScope.GrantedScopes[0].Actions = []string{"read", "write", "delete"}
	require.NotEqual(t, base, business.HopContentHash(widenedScope, "prev"))

	differentActor := baseHop()
	differentActor.ActorPrincipalID = "agent-2"
	require.NotEqual(t, base, business.HopContentHash(differentActor, "prev"))

	differentRevision := baseHop()
	differentRevision.AuthorizationRevision = 8
	require.NotEqual(t, base, business.HopContentHash(differentRevision, "prev"))

	// Folding a different parent hash yields a different address — the chain.
	require.NotEqual(t, base, business.HopContentHash(hop, "other-prev"))
}

func TestHopContentHash_ScopeSeparatorsCannotBeForged(t *testing.T) {
	// Two actions must not hash-collide with one action that embeds the join
	// separator, and a resource-kind boundary must not be crossable by content.
	twoActions := baseHop()
	twoActions.GrantedScopes = []business.ActorChainScope{
		{ResourceKind: "repo", Actions: []string{"a", "b"}},
	}
	oneJoinedAction := baseHop()
	oneJoinedAction.GrantedScopes = []business.ActorChainScope{
		{ResourceKind: "repo", Actions: []string{"a\x1eb"}},
	}
	require.NotEqual(t,
		business.HopContentHash(twoActions, ""),
		business.HopContentHash(oneJoinedAction, ""),
	)
}

func TestActorChainToRFC8693Subject_OwnerActingDirectly(t *testing.T) {
	subject := business.ActorChainToRFC8693Subject("owner-1", nil)
	require.Equal(t, "owner-1", subject.Sub)
	require.Nil(t, subject.Act, "no actors means the owner acts directly")
}

func TestActorChainToRFC8693Subject_NestingOrder(t *testing.T) {
	// Token carries actors earliest→current: [service, agent]. RFC 8693 puts the
	// current actor outermost and the earliest actor most deeply nested.
	subject := business.ActorChainToRFC8693Subject("owner-1", []string{"service-1", "agent-1"})

	require.Equal(t, "owner-1", subject.Sub)
	require.NotNil(t, subject.Act)
	require.Equal(t, "agent-1", subject.Act.Sub, "current actor is outermost")
	require.NotNil(t, subject.Act.Act)
	require.Equal(t, "service-1", subject.Act.Act.Sub, "earliest actor is most nested")
	require.Nil(t, subject.Act.Act.Act)
}

func TestActorChainToRFC8693Subject_JSONShape(t *testing.T) {
	subject := business.ActorChainToRFC8693Subject("owner-1", []string{"service-1", "agent-1"})
	encoded, err := json.Marshal(subject)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"sub":"owner-1","act":{"sub":"agent-1","act":{"sub":"service-1"}}}`,
		string(encoded),
	)
}
