package business

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The running column answers "is what runs what was declared?" Only two known
// executions get a verdict; an unobserved one is never reported as a match.
func TestCatalogueRunningVerdict(t *testing.T) {
	declared := &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3}
	cases := []struct {
		name     string
		declared *gen.CatalogueExecution
		observed *gen.CatalogueExecution
		verdict  gen.CatalogueRunningVerdict
		gap      gen.CatalogueGapReason
	}{
		{name: "nothing observed", declared: declared, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "nothing declared or observed", gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{
			name:     "observed without a declaration",
			observed: &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3},
			gap:      gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
		},
		{
			name:     "same digest and incarnation",
			declared: declared,
			observed: &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 3},
			verdict:  gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_MATCHES,
		},
		{
			name:     "another image",
			declared: declared,
			observed: &gen.CatalogueExecution{ImageDigest: "sha256:bbb", BuildIncarnation: 3},
			verdict:  gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_DIFFERS,
		},
		{
			// The incarnation moves when command, configuration or identity
			// change under the same image, so the image alone does not match.
			name:     "same image, another incarnation",
			declared: declared,
			observed: &gen.CatalogueExecution{ImageDigest: "sha256:aaa", BuildIncarnation: 2},
			verdict:  gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_DIFFERS,
		},
		{
			name:     "two empty digests are not a match",
			declared: &gen.CatalogueExecution{},
			observed: &gen.CatalogueExecution{},
			verdict:  gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_DIFFERS,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &gen.CatalogueEntry{}
			setCatalogueRunning(entry, tc.declared, tc.observed)
			if tc.gap != gen.CatalogueGapReason_CATALOGUE_GAP_REASON_UNSPECIFIED {
				require.Nil(t, entry.GetRunning())
				require.Equal(t, tc.gap, entry.GetRunningGap().GetReason())
				require.NotEmpty(t, entry.GetRunningGap().GetDetail())
				return
			}
			require.Nil(t, entry.GetRunningGap())
			require.Equal(t, tc.verdict, entry.GetRunning().GetVerdict())
			require.Same(t, tc.declared, entry.GetRunning().GetDeclared())
			require.Same(t, tc.observed, entry.GetRunning().GetObserved())
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
		ExposedTeams: []*gen.CollectionReadGrant{{
			Grant:        &gen.ScopeGrant{SubjectId: "team-" + id, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM},
			SubjectLabel: "Team " + id,
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

	// Every entry reports the presence-borne facts as gaps, never as values.
	for _, entry := range entries {
		e := entry.Entry
		notRecorded := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED
		require.Equal(t, notRecorded, e.GetDeclaredReleaseGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetBuildDigestGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetGenerationGap().GetReason(), e.GetName())
		require.Equal(t, notRecorded, e.GetBuildSizeGap().GetReason(), e.GetName())
		require.Contains(t, e.GetBuildSizeGap().GetDetail(), "codefly-dev/core#708")
		require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, e.GetRunningGap().GetReason(), e.GetName())
		require.Nil(t, e.GetRunning(), "%s: an unobserved entry has no verdict", e.GetName())
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
	require.Equal(t, "Team i1", first.GetExposedTeams()[0].GetSubjectLabel())
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
