package business

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	deployment "github.com/codefly-dev/cli/contracts/deployment"
	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The platform Catalogue (issue #1020) is a projection over records this host
// already keeps — the durable solution registry, the installations table and the
// composition's module principal registry. It adds no store of its own.
//
// Each entry is placed in the registry's state model — desired, authorized,
// applied, observed, withdrawing, physically retired — each state reported
// separately. Most of those facts have no record on this host yet. Each is
// reported as a CatalogueGap naming why, never as an empty value: an unrecorded
// revision is not revision 0, an unreported build size is not zero lines, an
// unobserved deployment is not one that matches, and an unrecorded approval is
// not a refusal.

// CatalogueInstallationRecord is one active installation as the Catalogue reads
// it: the installation, its organization's name, the identifier of the agent
// principal it was installed as, and the team grants that reach inside it —
// those made at its authority root apart from those inherited from above it.
type CatalogueInstallationRecord struct {
	Installation    *gen.Installation
	OrgName         string
	AgentIdentifier string
	GrantedTeams    []*gen.CollectionReadGrant
	InheritedTeams  []*gen.CollectionReadGrant
}

// PlatformCatalogueEntry is one Catalogue row. Registration is kept as the
// business record so the transport projects it through the same mapping every
// other registry response uses; Entry carries everything else.
type PlatformCatalogueEntry struct {
	Entry        *gen.CatalogueEntry
	Registration *SolutionRegistration
}

// PlatformCatalogue is the whole Catalogue at one registry revision.
// ApprovedInventories holds, by digest, the canonical bytes of every approved
// execution inventory an entry's authorization names; no record carries an
// approval yet, so it is empty.
type PlatformCatalogue struct {
	Entries             []*PlatformCatalogueEntry
	RegistryRevision    int64
	ApprovedInventories map[string][]byte
}

const (
	catalogueNoDeclaration   = "This host holds no applied presence document for this entry, which is what records what the composition declares."
	catalogueNoApplication   = "This host has applied no presence generation for this entry."
	catalogueNoApproval      = "This host holds no approval record: signed platform approval of the execution inventory, bound to target and ownership scope, has not been built yet."
	catalogueNotObserved     = "This host reads no observed cluster state, so what runs is unknown — never assumed to match."
	catalogueNoVerdict       = "There is no approval record to judge the observed execution against."
	catalogueNoWithdrawal    = "This host holds no withdrawal record; withdrawal is a change to the approval record, which does not exist yet."
	catalogueNoRevocation    = "This host holds no credential-revocation record for this entry."
	catalogueNoRetirement    = "No retirement controller reports to this host; without stop or fence evidence nothing is reported as retired."
	catalogueNoBuildSize     = "Build size is the presence document's build_size section (codefly-dev/core#708), computed at build by the CLI; this host holds no presence document for this entry."
	catalogueModulePublisher = "A composed module is known by its principal prefix alone; the composition records no publisher."
	catalogueUnregistered    = "No registration exists under this identifier; it is named only by its installations."
	catalogueNoRevision      = "Installations carry no revision of their own on this host."
	catalogueNoAgentRelease  = "The installation's agent principal carries no publisher/name:version identifier."
)

// ListPlatformCatalogue reads the Catalogue for a super administrator.
func (s *Service) ListPlatformCatalogue(ctx context.Context, actorID string, includeTombstoned bool) (*PlatformCatalogue, error) {
	w := wool.Get(ctx).In("ListPlatformCatalogue")
	if err := s.requirePlatformRole(ctx, actorID, "super_admin"); err != nil {
		return nil, NewStoreError(err, ErrTypePermission)
	}
	var (
		registrations []*SolutionRegistration
		revision      int64
		installations []*CatalogueInstallationRecord
	)
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		// Tombstones are always read: an installation of a deregistered solution
		// is shown beside the tombstone, not as a solution nobody registered.
		if registrations, revision, err = s.store.ListSolutionRegistrations(ctx, true); err != nil {
			return err
		}
		installations, err = s.installationStore().ListCatalogueInstallations(ctx)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot read the platform catalogue")
	}
	return &PlatformCatalogue{
		Entries:          projectPlatformCatalogue(s.modulePrincipals, registrations, installations, includeTombstoned),
		RegistryRevision: revision,
	}, nil
}

// projectPlatformCatalogue builds the Catalogue's rows: one per composed module,
// then one per solution identifier that a registration or an active
// installation names, each ordered by name. A tombstoned registration is listed
// only when asked for, or when an installation still names it.
func projectPlatformCatalogue(
	modules ModulePrincipalRegistry,
	registrations []*SolutionRegistration,
	installations []*CatalogueInstallationRecord,
	includeTombstoned bool,
) []*PlatformCatalogueEntry {
	var moduleEntries []*PlatformCatalogueEntry
	for _, grant := range modules {
		entry := newCatalogueEntry(gen.CatalogueEntryKind_CATALOGUE_ENTRY_KIND_MODULE, grant.Prefix)
		entry.PublisherValue = &gen.CatalogueEntry_PublisherGap{PublisherGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueModulePublisher)}
		moduleEntries = append(moduleEntries, &PlatformCatalogueEntry{Entry: entry})
	}

	solutions := make(map[string]*PlatformCatalogueEntry)
	solution := func(name string) *PlatformCatalogueEntry {
		if existing, ok := solutions[name]; ok {
			return existing
		}
		entry := newCatalogueEntry(gen.CatalogueEntryKind_CATALOGUE_ENTRY_KIND_SOLUTION, name)
		entry.PublisherValue = &gen.CatalogueEntry_PublisherGap{PublisherGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueUnregistered)}
		solutions[name] = &PlatformCatalogueEntry{Entry: entry}
		return solutions[name]
	}
	for _, installation := range installations {
		row := solution(installation.Installation.GetSolutionIdentifier())
		row.Entry.Installations = append(row.Entry.Installations, catalogueInstallation(installation))
	}
	for _, registration := range registrations {
		_, installed := solutions[registration.SolutionID]
		if registration.TombstonedAt != nil && !includeTombstoned && !installed {
			continue
		}
		row := solution(registration.SolutionID)
		row.Registration = registration
		row.Entry.PublisherValue = &gen.CatalogueEntry_Publisher{Publisher: registration.Publisher}
	}

	byName := func(a, b *PlatformCatalogueEntry) int { return strings.Compare(a.Entry.Name, b.Entry.Name) }
	slices.SortFunc(moduleEntries, byName)
	solutionEntries := make([]*PlatformCatalogueEntry, 0, len(solutions))
	for _, row := range solutions {
		solutionEntries = append(solutionEntries, row)
	}
	slices.SortFunc(solutionEntries, byName)
	return append(moduleEntries, solutionEntries...)
}

// newCatalogueEntry is an entry whose state facts are all gaps: no record on
// this host carries a declaration, an approval, an applied generation, an
// observation, a withdrawal, a retirement or a build size yet. When one does,
// the entry reads it here and the gap gives way to the value.
func newCatalogueEntry(kind gen.CatalogueEntryKind, name string) *gen.CatalogueEntry {
	notRecorded := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED
	notObserved := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED
	entry := &gen.CatalogueEntry{
		Kind: kind,
		Name: name,
		Desired: &gen.CatalogueDesired{
			DeclaredRevisionValue: &gen.CatalogueDesired_DeclaredRevisionGap{DeclaredRevisionGap: catalogueGap(notRecorded, catalogueNoDeclaration)},
			DeclaredReleaseValue:  &gen.CatalogueDesired_DeclaredReleaseGap{DeclaredReleaseGap: catalogueGap(notRecorded, catalogueNoDeclaration)},
		},
		Authorized: &gen.CatalogueAuthorized{
			AuthorizationValue: &gen.CatalogueAuthorized_AuthorizationGap{AuthorizationGap: catalogueGap(notRecorded, catalogueNoApproval)},
		},
		Applied: &gen.CatalogueApplied{
			AppliedRevisionValue: &gen.CatalogueApplied_AppliedRevisionGap{AppliedRevisionGap: catalogueGap(notRecorded, catalogueNoApplication)},
		},
		Observed: &gen.CatalogueObserved{
			ObservedRevisionValue:     &gen.CatalogueObserved_ObservedRevisionGap{ObservedRevisionGap: catalogueGap(notObserved, catalogueNotObserved)},
			ObservationFreshnessValue: &gen.CatalogueObserved_ObservationFreshnessGap{ObservationFreshnessGap: catalogueGap(notObserved, catalogueNotObserved)},
			ObservedExecutionValue:    &gen.CatalogueObserved_ObservedExecutionGap{ObservedExecutionGap: catalogueGap(notObserved, catalogueNotObserved)},
		},
		Withdrawing: &gen.CatalogueWithdrawing{
			WithdrawalStateValue:           &gen.CatalogueWithdrawing_WithdrawalStateGap{WithdrawalStateGap: catalogueGap(notRecorded, catalogueNoWithdrawal)},
			CredentialRevocationStateValue: &gen.CatalogueWithdrawing_CredentialRevocationStateGap{CredentialRevocationStateGap: catalogueGap(notRecorded, catalogueNoRevocation)},
		},
		Retired: &gen.CatalogueRetired{
			RetirementStateValue: &gen.CatalogueRetired_RetirementStateGap{RetirementStateGap: catalogueGap(notObserved, catalogueNoRetirement)},
		},
		BuildSizeValue: &gen.CatalogueEntry_BuildSizeGap{BuildSizeGap: catalogueGap(notRecorded, catalogueNoBuildSize)},
	}
	judgeCatalogueObserved(entry.Authorized, entry.Observed, nil)
	return entry
}

// judgeCatalogueObserved sets the observed verdict from the authorized and
// observed states the entry already carries. It judges what runs against what
// is authorized, never against what is declared, and gives a verdict only when
// both are known:
//
//   - no observation: NOT_OBSERVED, never a match;
//   - an observation with no current authorization: RUNNING_UNAUTHORIZED;
//   - an observation, but no authorization this host can read: NOT_RECORDED;
//   - an observation missing an approved container: NOT_OBSERVED, because an
//     incomplete observation is never counted as approved execution;
//   - every approved container on its approved digest, nothing else running,
//     and the approved incarnation: RUNNING_AUTHORIZED;
//   - anything else: RUNNING_DIFFERS.
//
// The approved execution is read from the approved inventory through the
// deployment contract, keyed by the inventory's digest in inventories.
func judgeCatalogueObserved(authorized *gen.CatalogueAuthorized, observed *gen.CatalogueObserved, inventories map[string][]byte) {
	gap := func(reason gen.CatalogueGapReason, detail string) {
		observed.VerdictValue = &gen.CatalogueObserved_VerdictGap{VerdictGap: catalogueGap(reason, detail)}
	}
	verdict := func(v gen.CatalogueObservedVerdict) {
		observed.VerdictValue = &gen.CatalogueObserved_Verdict{Verdict: v}
	}
	running := observed.GetObservedExecution()
	switch {
	case running == nil:
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, catalogueNotObserved)
		return
	case authorized.GetNotAuthorized() != nil:
		verdict(gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_UNAUTHORIZED)
		return
	case authorized.GetAuthorization() == nil:
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueNoVerdict)
		return
	}
	approval := authorized.GetAuthorization()
	approved, err := approvedContainerImageDigests(inventories[approval.GetInventoryDigest()], approval.GetInventoryDigest(), approval.GetMemberBinding())
	if err != nil {
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, "The approved inventory cannot be read: "+err.Error())
		return
	}
	observedDigests := running.GetContainerImageDigests()
	for key := range approved {
		if _, ok := observedDigests[key]; !ok {
			gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, "The observation is incomplete: approved container "+key+" was not observed.")
			return
		}
	}
	if maps.Equal(approved, observedDigests) && running.GetBuildIncarnation() == approval.GetBuildIncarnation() {
		verdict(gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED)
		return
	}
	verdict(gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS)
}

// approvedContainerImageDigests reads one member's approved containers out of
// an approved inventory, as "<workload id>/<container name>" → image manifest
// digest. Everything comes from the deployment contract: Check accepts the
// inventory's intrinsic form, its digest must be the one approval signed, and
// the containers are the contract's own seven-key projection (Rows), never a
// traversal of the workload templates written here.
func approvedContainerImageDigests(canonical []byte, digest, member string) (map[string]string, error) {
	if len(canonical) == 0 {
		return nil, fmt.Errorf("no inventory is held for %s", digest)
	}
	checked, err := deployment.Check(canonical)
	if err != nil {
		return nil, fmt.Errorf("the deployment contract refuses it: %w", err)
	}
	if got := checked.Digest(); got != digest {
		return nil, fmt.Errorf("its canonical bytes hash to %s, not the approved %s", got, digest)
	}
	inventory := checked.Inventory()
	var workloads []string
	for _, candidate := range inventory.Members {
		if candidate.Binding == member {
			workloads = candidate.Workloads
		}
	}
	if workloads == nil {
		return nil, fmt.Errorf("it has no member %q", member)
	}
	// The projection is one row per workload, in the inventory's workload
	// order; a row does not name its workload, so the pairing is by position.
	rows := checked.Rows()
	if len(rows) != len(inventory.Workloads) {
		return nil, fmt.Errorf("the contract projected %d rows for %d workloads", len(rows), len(inventory.Workloads))
	}
	out := make(map[string]string)
	for i, workload := range inventory.Workloads {
		if !slices.Contains(workloads, workload.ID) {
			continue
		}
		for name, reference := range rows[i].Images {
			_, imageDigest, ok := strings.Cut(reference, "@")
			if !ok {
				return nil, fmt.Errorf("container %s/%s has no image digest", workload.ID, name)
			}
			out[workload.ID+"/"+name] = imageDigest
		}
	}
	return out, nil
}

func catalogueInstallation(record *CatalogueInstallationRecord) *gen.CatalogueInstallation {
	out := &gen.CatalogueInstallation{
		Installation: record.Installation,
		OrgName:      record.OrgName,
		RevisionValue: &gen.CatalogueInstallation_RevisionGap{
			RevisionGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueNoRevision),
		},
		GrantedTeams:   record.GrantedTeams,
		InheritedTeams: record.InheritedTeams,
	}
	if release, ok := parseAgentRelease(record.AgentIdentifier); ok {
		out.AgentReleaseValue = &gen.CatalogueInstallation_AgentRelease{AgentRelease: release}
	} else {
		out.AgentReleaseValue = &gen.CatalogueInstallation_AgentReleaseGap{
			AgentReleaseGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueNoAgentRelease),
		}
	}
	return out
}

// parseAgentRelease splits a canonical "publisher/name:version" agent
// identifier at the same separators looksLikeAgentIdentifier admits it by.
func parseAgentRelease(identifier string) (*gen.CatalogueRelease, bool) {
	if !looksLikeAgentIdentifier(identifier) {
		return nil, false
	}
	slash := strings.IndexByte(identifier, '/')
	colon := strings.IndexByte(identifier, ':')
	return &gen.CatalogueRelease{
		Publisher: identifier[:slash],
		Name:      identifier[slash+1 : colon],
		Version:   identifier[colon+1:],
	}, true
}

func catalogueGap(reason gen.CatalogueGapReason, detail string) *gen.CatalogueGap {
	return &gen.CatalogueGap{Reason: reason, Detail: detail}
}
