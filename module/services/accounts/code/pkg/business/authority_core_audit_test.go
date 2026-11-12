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
	// Every kind the core journals is recorded here, but a module's delivery
	// and withdrawal, which the delivery path records itself.
	for _, kind := range []domain.EventKind{domain.InstallationRequested, domain.InstallationActivated, domain.InstallationUninstalled,
		domain.InstallationReconverged, domain.InstallationUpgradeAvailable, domain.InstallationUpgraded,
		domain.InstallationReachAdopted, domain.InstallationReachUnreached, domain.InstallationReachRestored,
		domain.InstallationRoleReachWidened, domain.RoleGranted, domain.RoleViewOpened, domain.RoleRevoked,
		domain.DelegationGranted, domain.DelegationRevoked, domain.ResourceAttached, domain.ResourceDetached, domain.ResourceTransferred} {
		_, recorded := authorityEvents[kind]
		require.True(t, recorded, "%s is a change of authority this host records", kind)
	}
	for _, kind := range []domain.EventKind{domain.ModuleApplied, domain.ModuleWithdrawn} {
		_, recorded := authorityEvents[kind]
		require.False(t, recorded, "%s is recorded by the delivery path (saas.solution.host_binding_applied), never twice", kind)
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

// A delivery that widens what a module page's roles reach ends their grants
// (role_revoked, actor delivery:<module>) and journals what widened. Without
// that record an administrator reads only the revocations, and a designed
// end of an unapproved widening looks like a delivery destroying grants.
func TestARoleReachWideningIsRecordedWithWhatWidened(t *testing.T) {
	mapped, recorded := authorityEvents[domain.InstallationRoleReachWidened]
	require.True(t, recorded)
	require.Equal(t, EventAuthorityInstallationRoleReachWidened, mapped.event)
	require.Equal(t, "installation", mapped.resource)
	// The detail the core journals (authority-core page_reach.go).
	e := domain.Event{Kind: domain.InstallationRoleReachWidened, Organization: "00000000-0000-7000-8000-0000000000a1", Subject: "00000000-0000-7000-8000-0000000000c1",
		Detail: map[string]string{"target": "org-directory", "delivered": "org-directory", "roles": "editor,installer,viewer",
			"widened": "role editor reaches org-directory > runtime (binding)", "revision": "2"}}
	payload, err := authorityPayload(nil, e)
	require.NoError(t, err)
	require.Equal(t, "editor,installer,viewer", payload["roles"])
	require.NoError(t, ValidatePayload(mapped.event, payload))
}
