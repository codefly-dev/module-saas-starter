package business

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	deployment "github.com/codefly-dev/cli/contracts/deployment"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

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
//
// The affirmative verdict carries the extra burden, because it is the one a
// reader acts on by doing nothing: it needs an approval complete enough to judge
// against and an observation whose age is known and current. Every case below
// states the observation's age, including the ones that are a match in every
// other respect and still must not come back green.
func TestCatalogueObservedVerdict(t *testing.T) {
	canonical, digest := executionInventory(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	// fresh and stale are ages, not instants: a verdict reads the observation's
	// age against the host's validity window, never a fixed date.
	fresh := timestamppb.New(now.Add(-time.Minute))
	stale := timestamppb.New(now.Add(-time.Hour))
	ahead := timestamppb.New(now.Add(time.Hour))
	inventories := map[string][]byte{digest: canonical}
	authorizedAs := func(inventoryDigest, member string, incarnation uint64) *gen.CatalogueAuthorized {
		return &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_Authorization{
			Authorization: &gen.CatalogueAuthorization{
				AuthorizedRevision: 4, InventoryDigest: inventoryDigest, MemberBinding: member, BuildIncarnation: incarnation,
			},
		}}
	}
	authorized := authorizedAs(digest, "member", 3)
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
		observedAt  *timestamppb.Timestamp
		inventories map[string][]byte
		verdict     gen.CatalogueObservedVerdict
		gap         gen.CatalogueGapReason
		detail      string
	}{
		{name: "authorized, nothing observed", authorized: authorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "not authorized, nothing observed", authorized: notAuthorized, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED},
		{name: "observed, authorization unknown to this host", authorized: unknown, observed: observedAll(3, nil), observedAt: fresh, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{
			name: "every approved container on its approved digest, at the approved incarnation, currently observed", authorized: authorized,
			observed: observedAll(3, nil), observedAt: fresh, verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED,
		},
		{
			// The construction that matters: everything matches, and the host
			// cannot say when. "Running authorized" beside "Not observed" is a
			// green an operator stops at, so there is no verdict at all.
			name: "the approved execution exactly, observed at an unknown time", authorized: authorized,
			observed: observedAll(3, nil), gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
			detail: "carries no time",
		},
		{
			name: "the approved execution exactly, observed an hour ago", authorized: authorized,
			observed: observedAll(3, nil), observedAt: stale, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
			detail: "past the 5m0s this host treats as current",
		},
		{
			// A stamp from the future is not evidence about the present either,
			// and a window that only looked backwards would accept it.
			name: "the approved execution exactly, observed an hour ahead of this host", authorized: authorized,
			observed: observedAll(3, nil), observedAt: ahead, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
			detail: "ahead of this host's clock",
		},
		{
			// This host assigns the incarnation from one, so an approval at zero
			// carries none. Left uncompared, an observation that also carries
			// none would match it.
			name: "an approval carrying no build incarnation", authorized: authorizedAs(digest, "member", 0),
			observed: observedAll(0, nil), observedAt: fresh, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
			detail: "no build incarnation",
		},
		{
			name: "one container on another image", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { m["worker/helper"] = "sha256:" + strings.Repeat("b", 64) }), observedAt: fresh,
			verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			name: "a container the inventory does not approve", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { m["worker/sidecar"] = inventoryImage }), observedAt: fresh,
			verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// The incarnation moves when command, configuration or identity
			// change under the same image, so the images alone do not match.
			name: "every image approved, another incarnation", authorized: authorized,
			observed: observedAll(2, nil), observedAt: fresh, verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			// A finished init container is still part of what ran.
			name: "an approved init container missing from the observation", authorized: authorized,
			observed: observedAll(3, func(m map[string]string) { delete(m, "worker/seed") }), observedAt: fresh,
			gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
		},
		{
			// The dangerous row: something runs that nothing currently
			// authorizes, whatever its image.
			name: "observed with no current authorization", authorized: notAuthorized,
			observed: observedAll(3, nil), observedAt: fresh, verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED,
		},
		{
			// The asymmetry, stated as a test: an age that withholds the green
			// verdict does not withhold the alarming ones. An old report of
			// something wrong is still a report of something wrong.
			name: "an hour-old observation of another incarnation", authorized: authorized,
			observed: observedAll(2, nil), observedAt: stale,
			verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS,
		},
		{
			name: "an hour-old observation of something nothing authorizes", authorized: notAuthorized,
			observed: observedAll(3, nil), observedAt: stale,
			verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED,
		},
		{name: "approved inventory not held", authorized: authorized, observed: observedAll(3, nil), observedAt: fresh, inventories: map[string][]byte{}, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{name: "inventory bytes that do not hash to the approved digest", authorized: authorizedAs(wrongDigest, "member", 3), observed: observedAll(3, nil), observedAt: fresh, inventories: map[string][]byte{wrongDigest: canonical}, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
		{
			// Its digest is right and its member and images are all there: only the
			// contract can say this is not an inventory it defines.
			name: "an inventory the contract refuses, held under its own digest", authorized: authorizedAs(otherSchemaDigest, "member", 3),
			observed: observedAll(3, nil), observedAt: fresh, inventories: map[string][]byte{otherSchemaDigest: otherSchema},
			gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
		},
		{name: "a member the inventory does not have", authorized: authorizedAs(digest, "someone-else", 3), observed: observedAll(3, nil), observedAt: fresh, gap: gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := &gen.CatalogueObserved{}
			if tc.observed != nil {
				observed.ObservedExecutionValue = &gen.CatalogueObserved_ObservedExecution{ObservedExecution: tc.observed}
			}
			if tc.observedAt != nil {
				observed.ObservationFreshnessValue = &gen.CatalogueObserved_ObservedAt{ObservedAt: tc.observedAt}
			} else {
				observed.ObservationFreshnessValue = &gen.CatalogueObserved_ObservationFreshnessGap{
					ObservationFreshnessGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, "no cluster state"),
				}
			}
			held := inventories
			if tc.inventories != nil {
				held = tc.inventories
			}
			judgeCatalogueObserved(tc.authorized, observed, held, now)
			if tc.gap != gen.CatalogueGapReason_CATALOGUE_GAP_REASON_UNSPECIFIED {
				require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_UNSPECIFIED, observed.GetVerdict())
				require.Equal(t, tc.gap, observed.GetVerdictGap().GetReason())
				require.NotEmpty(t, observed.GetVerdictGap().GetDetail())
				if tc.detail != "" {
					require.Contains(t, observed.GetVerdictGap().GetDetail(), tc.detail,
						"the gap must say which evidence was missing, not only that one was")
				}
				return
			}
			require.Nil(t, observed.GetVerdictGap(), observed.GetVerdictGap().GetDetail())
			require.Equal(t, tc.verdict, observed.GetVerdict())
		})
	}
}

// The emptiness guard in judgeCatalogueObserved cannot be reached through an
// inventory the contract accepts, and this is why: the contract refuses a
// workload with no application container. If that ever relaxes, an approved
// member with no container would equal an observation with none, so this test
// fails here rather than the page turning green there.
func TestExecutionInventoryContractRequiresAContainer(t *testing.T) {
	raw, err := os.ReadFile("testdata/execution-inventory-base.json")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	spec := doc["workloads"].([]any)[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["containers"] = []any{}
	spec["initContainers"] = []any{}
	empty, err := json.Marshal(doc)
	require.NoError(t, err)

	err = deployment.CheckSchema(empty)
	require.Error(t, err, "an inventory whose member approves no container must not be a valid inventory")
	require.Contains(t, err.Error(), "CONTAINER_REQUIRED")
}

// The read boundary withholds an affirmative verdict whose evidence is not
// beside it. judgeCatalogueObserved is the only author of a verdict and applies
// the same rule, so nothing should reach this check — but the states are filled
// from records that do not exist yet (applied presence, the signed approval,
// cluster observation), and a filler that set a value case without re-judging
// would otherwise publish a green cell. The boundary fails closed instead.
func TestCatalogueReadBoundaryWithholdsUnsupportedAffirmativeVerdict(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	authorized := func(incarnation uint64) *gen.CatalogueAuthorized {
		return &gen.CatalogueAuthorized{AuthorizationValue: &gen.CatalogueAuthorized_Authorization{
			Authorization: &gen.CatalogueAuthorization{AuthorizedRevision: 4, InventoryDigest: "sha256:x", MemberBinding: "member", BuildIncarnation: incarnation},
		}}
	}
	stampFreshness := func(observed *gen.CatalogueObserved, at *timestamppb.Timestamp) {
		if at == nil {
			observed.ObservationFreshnessValue = &gen.CatalogueObserved_ObservationFreshnessGap{
				ObservationFreshnessGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, "no cluster state"),
			}
			return
		}
		observed.ObservationFreshnessValue = &gen.CatalogueObserved_ObservedAt{ObservedAt: at}
	}
	for _, tc := range []struct {
		name       string
		authorized *gen.CatalogueAuthorized
		execution  *gen.CatalogueObservedExecution
		observedAt *timestamppb.Timestamp
		withheld   string
	}{
		{
			name: "fresh, complete and approved", authorized: authorized(3), execution: observedAll(3, nil),
			observedAt: timestamppb.New(now.Add(-time.Minute)),
		},
		{
			name: "no approval beside it", execution: observedAll(3, nil),
			observedAt: timestamppb.New(now.Add(-time.Minute)), withheld: "no approval",
		},
		{
			name: "an approval with no build incarnation", authorized: authorized(0), execution: observedAll(3, nil),
			observedAt: timestamppb.New(now.Add(-time.Minute)), withheld: "no build incarnation",
		},
		{
			name: "no observed execution beside it", authorized: authorized(3),
			observedAt: timestamppb.New(now.Add(-time.Minute)), withheld: "no observed execution",
		},
		{
			name: "an observation whose age is unknown", authorized: authorized(3), execution: observedAll(3, nil),
			withheld: "carries no time",
		},
		{
			name: "an hour-old observation", authorized: authorized(3), execution: observedAll(3, nil),
			observedAt: timestamppb.New(now.Add(-time.Hour)), withheld: "treats as current",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := &gen.CatalogueObserved{
				VerdictValue: &gen.CatalogueObserved_Verdict{Verdict: gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED},
			}
			stampFreshness(observed, tc.observedAt)
			if tc.execution != nil {
				observed.ObservedExecutionValue = &gen.CatalogueObserved_ObservedExecution{ObservedExecution: tc.execution}
			}
			entries := []*PlatformCatalogueEntry{{Entry: &gen.CatalogueEntry{Name: "entry", Authorized: tc.authorized, Observed: observed}}}

			enforceCatalogueVerdictEvidence(context.Background(), entries, now)

			if tc.withheld == "" {
				require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED, observed.GetVerdict(),
					"a verdict its own evidence supports is served as it stands")
				require.Nil(t, observed.GetVerdictGap())
				return
			}
			require.Equal(t, gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_UNSPECIFIED, observed.GetVerdict())
			require.Equal(t, gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, observed.GetVerdictGap().GetReason())
			require.Contains(t, observed.GetVerdictGap().GetDetail(), tc.withheld)
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

	entries := projectPlatformCatalogue(modules, registrations, installations, false, now)
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

	withTombstones := projectPlatformCatalogue(modules, registrations, installations, true, now)
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
