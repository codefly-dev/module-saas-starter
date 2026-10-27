package business

import (
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// Every change the core journals that this host records is a registered
// mutation, emitted on the core's own transaction, and recorded as a person
// or as the system: the values the audit column admits.
func TestAuthorityChangesAreRecordedAsRegisteredMutations(t *testing.T) {
	catalog := map[EventType]AuditEventDefinition{}
	for _, d := range AuditEventCatalog() {
		catalog[d.Type] = d
	}
	for kind, mapped := range authorityEvents {
		d, ok := catalog[mapped.event]
		require.True(t, ok, "%s records %s, which is not registered", kind, mapped.event)
		require.Equal(t, DurabilityTransactional, d.Durability, "%s records %s as a mutation", kind, mapped.event)
	}
	for _, kind := range []domain.EventKind{domain.InstallationRequested, domain.InstallationActivated, domain.InstallationUninstalled,
		domain.RoleGranted, domain.RoleRevoked, domain.DelegationGranted, domain.DelegationRevoked,
		domain.ResourceAttached, domain.ResourceDetached, domain.ResourceTransferred} {
		_, recorded := authorityEvents[kind]
		require.True(t, recorded, "%s is a change of authority this host records", kind)
	}
	id, actorType := authorityActor("00000000-0000-7000-8000-0000000000b1")
	require.Equal(t, "00000000-0000-7000-8000-0000000000b1", id)
	require.Equal(t, ActorTypeUser, actorType)
	id, actorType = authorityActor("documents")
	require.Empty(t, id)
	require.Equal(t, ActorTypeSystem, actorType)
	id, actorType = authorityActor("")
	require.Empty(t, id)
	require.Equal(t, ActorTypeSystem, actorType)
}
