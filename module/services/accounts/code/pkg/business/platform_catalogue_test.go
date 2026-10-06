package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The observed state's verdict answers "is what runs what is authorized?" It is
// judged against the approval, never the declaration, and given only when both
// an observation and a known authorization exist: an unobserved entry is never a
// match, and an entry the host cannot judge is never "unauthorized".
func TestCatalogueObservedVerdict(t *testing.T) {
	approved := &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3}
	authorized := &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_Authorization{
		Authorization: &gen.CatalogueAuthorization{AuthorizedRevision: 4, ApprovedExecution: approved},
	}}
	notAuthorized := &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_NotAuthorized{
		NotAuthorized: &gen.CatalogueNotAuthorized{Detail: "approval withdrawn"},
	}}
	unknown := &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_AuthorizationGap{
		AuthorizationGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, "no approval record"),
	}}
	cases := []struct {
		name       string
		authorized *gen.CatalogueAuthorized
		observed   *gen.CatalogueExecution
		verdict    gen.CatalogueObservedVerdict
		gap        gen.CatalogueGapReason
	}{
		{name: "authorized, nothing observed", authorized: authorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "not authorized, nothing observed", authorized: notAuthorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{
			name:       "observed, authorization unknown to this host",
			authorized: unknown,
			observed:   &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3},
			gap:        gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
		},
		{
			name:       "the approved digest and incarnation",
			authorized: authorized,
			observed:   &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3},
			verdict:    gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED,
		},
		{
			name:       "another image",
			authorized: authorized,
			observed:   &gen.CatalogueExecution{ImageDigest: "sha256:bbb", BuildIncarnation: 3},
			verdict:    gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// The incarnation moves when command, configuration or identity
			// change under the same image, so the image alone does not match.
			name:       "same image, another incarnation",
			authorized: authorized,
			observed:   &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 2},
			verdict:    gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			name: "two empty digests are not a match",
			authorized: &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_Authorization{
				Authorization: &gen.CatalogueAuthorization{ApprovedExecution: &gen.CatalogueExecution{}},
			}},
			observed: &gen.CatalogueExecution{},
			verdict:  gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// The dangerous row: something runs that nothing currently
			// authorizes, whatever its image.
			name:       "observed with no current authorization",
			authorized: notAuthorized,
			observed:   &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3},
			verdict:    gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := &gen.CatalogueObserved{}
			if tc.observed != nil {
				observed.ObservedExecutionValue = &gen.CatalogueObserved_ObservedExecution{ObservedExecution: tc.observed}
			}
			judgeCatalogueObserved(tc.authorized, observed)
			if tc.gap != gen.CatalogueGapReason_CATALOGUE_GAP_REASON_UNSPECIFIED {
				require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_UNSPECIFIED, observed.GetVerdict())
				require.Equal(t, tc.gap, observed.GetVerdictGap().GetReason())
				require.NotEmpty(t, observed.GetVerdictGap().GetDetail())
				return
			}
			require.Nil(t, observed.GetVerdictGap())
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
