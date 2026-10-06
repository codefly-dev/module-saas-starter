package business

import (
	"os"
	"strings"
	"testing"
	"time"

	deployment "github.com/codefly-dev/cli/contracts/deployment"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// executionInventory is the deployment contract's own base inventory
// (contracts/deployment/testdata/base.json in codefly-dev/cli), copied as test
// data. Every test reads it through the contract, so a copy the contract no
// longer accepts fails here rather than drifting: one member, "member", whose
// workload "worker" runs app containers helper and worker and init containers
// prepare and seed, all on one image digest.
func executionInventory(t *testing.T) (canonical []byte, digest string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/execution-inventory-base.json")
	require.NoError(t, err)
	require.NoError(t, deployment.CheckSchema(raw), "refresh testdata from the deployment contract")
	canonical, err = deployment.CanonicalJSON(raw)
	require.NoError(t, err)
	return canonical, deployment.Digest(canonical)
}

const inventoryImage = "sha256:227abccb20fc21ca22053afa8770ffeaeee9f48848f5dd55fdd8cd3d185e8e77"

func observedAll(incarnation uint64, edit func(map[string]string)) *gen.CatalogueObservedExecution {
	digests := map[string]string{
		"worker/helper": inventoryImage, "worker/worker": inventoryImage,
		"worker/prepare": inventoryImage, "worker/seed": inventoryImage,
	}
	if edit != nil {
		edit(digests)
	}
	return &gen.CatalogueObservedExecution{ContainerImageDigests: digests, BuildIncarnation: incarnation}
}

// The observed state's verdict answers "is what runs what is authorized?" It is
// judged against the approved inventory, read through the deployment contract,
// never against the declaration, and given only when a complete observation and
// a readable authorization both exist: an unobserved or partly observed entry is
// never a match, and an entry the host cannot judge is never "unauthorized".
func TestCatalogueObservedVerdict(t *testing.T) {
	canonical, digest := executionInventory(t)
	inventories := map[string][]byte{digest: canonical}
	authorizedAs := func(inventoryDigest, member string) *gen.CatalogueAuthorized {
		return &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_Authorization{
			Authorization: &gen.CatalogueAuthorization{
				AuthorizedRevision: 4, InventoryDigest: inventoryDigest, MemberBinding: member, BuildIncarnation: 3,
			},
		}}
	}
	authorized := authorizedAs(digest, "member")
	notAuthorized := &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_NotAuthorized{
		NotAuthorized: &gen.CatalogueNotAuthorized{Detail: "approval withdrawn"},
	}}
	unknown := &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_AuthorizationGap{
		AuthorizationGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, "no approval record"),
	}}
	// A valid inventory the contract accepts, held under a digest it does not
	// hash to: what a record that swapped the approved bytes would present.
	wrongDigest := "sha256:" + strings.Repeat("0", 64)
	otherSchema := []byte(strings.Replace(string(canonical), `"codefly/execution-inventory/v1"`, `"codefly/execution-inventory/v0"`, 1))
	require.NotEqual(t, canonical, otherSchema)
	otherSchemaDigest := deployment.Digest(otherSchema)

	cases := []struct {
		name        string
		authorized  *gen.CatalogueAuthorized
		observed    *gen.CatalogueObservedExecution
		inventories map[string][]byte
		verdict     gen.CatalogueObservedVerdict
		gap         gen.CatalogueGapReason
	}{
		{name: "authorized, nothing observed", authorized: authorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "not authorized, nothing observed", authorized: notAuthorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "observed, authorization unknown to this host", authorized: unknown, observed: observedAll(3, nil), gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{
			name: "every approved container on its approved digest, at the approved incarnation", authorized: authorized,
			observed: observedAll(3, nil), verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED,
		},
		{
			name: "one container on another image", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { m["worker/helper"] = "sha256:" + strings.Repeat("b", 64) }),
			verdict:  gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			name: "a container the inventory does not approve", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { m["worker/sidecar"] = inventoryImage }),
			verdict:  gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// The incarnation moves when command, configuration or identity
			// change under the same image, so the images alone do not match.
			name: "every image approved, another incarnation", authorized: authorized,
			observed: observedAll(2, nil), verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// A finished init container is still part of what ran.
			name: "an approved init container missing from the observation", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { delete(m, "worker/seed") }),
			gap:      gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
		},
		{
			// The dangerous row: something runs that nothing currently
			// authorizes, whatever its image.
			name: "observed with no current authorization", authorized: notAuthorized,
			observed: observedAll(3, nil), verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED,
		},
		{name: "approved inventory not held", authorized: authorized, observed: observedAll(3, nil), inventories: map[string][]byte{}, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{name: "inventory bytes that do not hash to the approved digest", authorized: authorizedAs(wrongDigest, "member"), observed: observedAll(3, nil), inventories: map[string][]byte{wrongDigest: canonical}, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{
			// Its digest is right and its member and images are all there: only the
			// contract can say this is not an inventory it defines.
			name: "an inventory the contract refuses, held under its own digest", authorized: authorizedAs(otherSchemaDigest, "member"),
			observed: observedAll(3, nil), inventories: map[string][]byte{otherSchemaDigest: otherSchema},
			gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
		},
		{name: "a member the inventory does not have", authorized: authorizedAs(digest, "someone-else"), observed: observedAll(3, nil), gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := &gen.CatalogueObserved{}
			if tc.observed != nil {
				observed.ObservedExecutionValue = &gen.CatalogueObserved_ObservedExecution{ObservedExecution: tc.observed}
			}
			held := inventories
			if tc.inventories != nil {
				held = tc.inventories
			}
			judgeCatalogueObserved(tc.authorized, observed, held)
			if tc.gap != gen.CatalogueGapReason_CATALOGUE_GAP_REASON_UNSPECIFIED {
				require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_UNSPECIFIED, observed.GetVerdict())
				require.Equal(t, tc.gap, observed.GetVerdictGap().GetReason())
				require.NotEmpty(t, observed.GetVerdictGap().GetDetail())
				return
			}
			require.Nil(t, observed.GetVerdictGap(), observed.GetVerdictGap().GetDetail())
			require.Equal(t, tc.verdict, observed.GetVerdict())
		})
	}
}

func catalogueTestInstallation(id, solution, org, agent string) *CatalogueInstallationRecord {
	return &CatalogueInstallationRecord{
		Installation: &gen.Installation{
			Id: id, OrgId: org + "-id", SolutionIdentifier: solution,
			Status: gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE,
		},
		OrgName:         org,
		AgentIdentifier: agent,
		GrantedTeams: []*gen.CollectionReadGrant{{
			Grant:        &gen.ScopeGrant{SubjectId: "team-" + id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM},
			SubjectLabel: "Team " + id,
		}},
		InheritedTeams: []*gen.CollectionReadGrant{{
			Grant:        &gen.ScopeGrant{SubjectId: "org-team-" + id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM},
			SubjectLabel: "Org team " + id,
		}},
	}
}

func catalogueEntryNames(entries []*PlatformCatalogueEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Entry.GetKind().String()+":"+entry.Entry.GetName())
	}
	return names
}

func TestProjectPlatformCatalogue(t *testing.T) {
	now := time.Now()
	modules := ModulePrincipalRegistry{
		ModulePrincipalID("zeta"):  {Prefix: "zeta"},
		ModulePrincipalID("alpha"): {Prefix: "alpha"},
	}
	registrations := []*SolutionRegistration{
		{SolutionID: "registered", Publisher: "solution:registered", Revision: 4},
		{SolutionID: "installed", Publisher: "solution:installed", Revision: 7},
		{SolutionID: "retired", Publisher: "solution:retired", Revision: 9, TombstonedAt: &now},
		{SolutionID: "retired-but-installed", Publisher: "solution:retired-but-installed", Revision: 11, TombstonedAt: &now},
	}
	installations := []*CatalogueInstallationRecord{
		catalogueTestInstallation("i1", "installed", "Acme", "example/installed:1.2.0"),
		catalogueTestInstallation("i2", "installed", "ExampleCorp", "example/installed:1.3.0"),
		catalogueTestInstallation("i3", "unregistered", "Acme", "not-an-identifier"),
		catalogueTestInstallation("i4", "retired-but-installed", "Acme", "example/retired:0.9.0"),
	}

	entries := projectPlatformCatalogue(modules, registrations, installations, false)
	require.Equal(t, []string{
		"CATALOGUE_ENTRY_KIND_MODULE:alpha",
		"CATALOGUE_ENTRY_KIND_MODULE:zeta",
		"CATALOGUE_ENTRY_KIND_SOLUTION:installed",
		"CATALOGUE_ENTRY_KIND_SOLUTION:registered",
		"CATALOGUE_ENTRY_KIND_SOLUTION:retired-but-installed",
		"CATALOGUE_ENTRY_KIND_SOLUTION:unregistered",
	}, catalogueEntryNames(entries), "modules first, then solutions, each by name; an uninstalled tombstone is omitted")

	byName := make(map[string]*PlatformCatalogueEntry)
	for _, entry := range entries {
		byName[entry.Entry.GetName()] = entry
	}

	// Every entry reports each state's facts as gaps, never as values: no
	// record on this host carries them yet, so nothing reads as authorized,
	// applied, running, withdrawn or retired.
	for _, entry := range entries {
		e := entry.Entry
		notRecorded := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED
		notObserved := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED
		require.Equal(t, notRecorded, e.GetDesired().GetDeclaredRevisionGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetDesired().GetDeclaredReleaseGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetAuthorized().GetAuthorizationGap().GetReason(), e.GetName())
		require.Nil(t, e.GetAuthorized().GetNotAuthorized(), "%s: an unrecorded approval is not a refusal", e.GetName())
		require.Equal(t, notRecorded, e.GetApplied().GetAppliedRevisionGap().GetReason(), e.GetName())
		require.Equal(t, notObserved, e.GetObserved().GetObservedRevisionGap().GetReason(), e.GetName())
		require.Equal(t, notObserved, e.GetObserved().GetObservationFreshnessGap().GetReason(), e.GetName())
		require.Equal(t, notObserved, e.GetObserved().GetObservedExecutionGap().GetReason(), e.GetName())
		require.Equal(t, notObserved, e.GetObserved().GetVerdictGap().GetReason(), e.GetName())
		require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_UNSPECIFIED, e.GetObserved().GetVerdict(),
			"%s: an unobserved entry has no verdict", e.GetName())
		require.Equal(t, notRecorded, e.GetWithdrawing().GetWithdrawalStateGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetWithdrawing().GetCredentialRevocationStateGap().GetReason(), e.GetName())
		require.Equal(t, notObserved, e.GetRetired().GetRetirementStateGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetBuildSizeGap().GetReason(), e.GetName())
		require.Contains(t, e.GetBuildSizeGap().GetDetail(), "codefly-dev/core#708")
	}

	module := byName["alpha"]
	require.Nil(t, module.Registration)
	require.Empty(t, module.Entry.GetInstallations())
	require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, module.Entry.GetPublisherGap().GetReason())

	registered := byName["registered"]
	require.Same(t, registrations[0], registered.Registration)
	require.Equal(t, "solution:registered", registered.Entry.GetPublisher())
	require.Empty(t, registered.Entry.GetInstallations())

	installed := byName["installed"]
	require.Same(t, registrations[1], installed.Registration)
	require.Len(t, installed.Entry.GetInstallations(), 2)
	first := installed.Entry.GetInstallations()[0]
	require.Equal(t, "i1", first.GetInstallation().GetId())
	require.Equal(t, "Acme", first.GetOrgName())
	require.Equal(t, &gen.CatalogueRelease{Publisher: "example", Name: "installed", Version: "1.2.0"}, first.GetAgentRelease())
	require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, first.GetRevisionGap().GetReason())
	require.Equal(t, "Team i1", first.GetGrantedTeams()[0].GetSubjectLabel())
	require.Equal(t, "Org team i1", first.GetInheritedTeams()[0].GetSubjectLabel(), "inherited reach is carried apart from what was granted here")
	require.Equal(t, "1.3.0", installed.Entry.GetInstallations()[1].GetAgentRelease().GetVersion())

	unregistered := byName["unregistered"]
	require.Nil(t, unregistered.Registration)
	require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, unregistered.Entry.GetPublisherGap().GetReason())
	require.Contains(t, unregistered.Entry.GetPublisherGap().GetDetail(), "No registration")
	require.Nil(t, unregistered.Entry.GetInstallations()[0].GetAgentRelease())
	require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
		unregistered.Entry.GetInstallations()[0].GetAgentReleaseGap().GetReason())

	retired := byName["retired-but-installed"]
	require.Same(t, registrations[3], retired.Registration, "an installation of a deregistered solution is shown beside its tombstone")
	require.Len(t, retired.Entry.GetInstallations(), 1)

	withTombstones := projectPlatformCatalogue(modules, registrations, installations, true)
	require.Contains(t, catalogueEntryNames(withTombstones), "CATALOGUE_ENTRY_KIND_SOLUTION:retired")
}

func TestParseAgentRelease(t *testing.T) {
	release, ok := parseAgentRelease("example.io/billing:2.0.1")
	require.True(t, ok)
	require.Equal(t, &gen.CatalogueRelease{Publisher: "example.io", Name: "billing", Version: "2.0.1"}, release)

	for _, identifier := range []string{"", "billing", "example/billing", "example/billing:", "/billing:1", "example:1/billing"} {
		_, ok := parseAgentRelease(identifier)
		require.False(t, ok, identifier)
	}
}
