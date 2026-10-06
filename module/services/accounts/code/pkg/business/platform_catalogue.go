package business

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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

// catalogueObservationValidity is how long an observation may be used to say
// what runs *now*, and catalogueObservationSkew how far ahead of this host's
// clock an observer's stamp may be before it stops being evidence at all.
//
// Only the affirmative verdict needs either: a reassuring answer read off an
// observation that no longer describes the present is the one failure this page
// cannot afford, while an answer that reports trouble is worth showing whatever
// its age. The observing producer declares no validity of its own yet; when its
// record carries one, that bound replaces these and this host stops choosing.
const (
	catalogueObservationValidity = 5 * time.Minute
	catalogueObservationSkew     = 30 * time.Second
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
	now := time.Now()
	entries := projectPlatformCatalogue(s.modulePrincipals, registrations, installations, includeTombstoned, now)
	enforceCatalogueVerdictEvidence(ctx, entries, now)
	return &PlatformCatalogue{
		Entries:          entries,
		RegistryRevision: revision,
	}, nil
}

// enforceCatalogueVerdictEvidence is the read boundary refusing to serve an
// affirmative verdict that the evidence beside it does not support. Nothing
// should reach it: judgeCatalogueObserved is the only author of a verdict, and
// it applies the same rule. But the states are filled from records this host
// does not hold yet — applied presence (#953), the deployment contract's signed
// approval, cluster observation — and any of those fillers could set a value
// case without re-judging. An operator reads the green and stops looking, so the
// boundary fails closed: the verdict becomes a gap naming what was missing, and
// the inconsistency is logged as the defect it is rather than rendered.
func enforceCatalogueVerdictEvidence(ctx context.Context, entries []*PlatformCatalogueEntry, now time.Time) {
	for _, row := range entries {
		observed := row.Entry.GetObserved()
		if observed.GetVerdict() != gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED {
			continue
		}
		var reason string
		switch approval := row.Entry.GetAuthorized().GetAuthorization(); {
		case approval == nil:
			reason = "It carries no approval for an observation to be authorized against."
		case approval.GetBuildIncarnation() == 0:
			reason = "Its approval carries no build incarnation."
		case observed.GetObservedExecution() == nil:
			reason = "It carries no observed execution."
		default:
			reason = catalogueObservationNotCurrent(observed, now)
		}
		if reason == "" {
			continue
		}
		wool.Get(ctx).In("ListPlatformCatalogue").Error(
			"withholding an affirmative catalogue verdict the entry's own evidence does not support",
			wool.Field("entry", row.Entry.GetName()),
			wool.Field("reason", reason))
		observed.VerdictValue = &gen.CatalogueObserved_VerdictGap{VerdictGap: catalogueGap(
			gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED,
			"This host withheld an affirmative verdict that its own evidence does not support. "+reason)}
	}
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
	now time.Time,
) []*PlatformCatalogueEntry {
	var moduleEntries []*PlatformCatalogueEntry
	for _, grant := range modules {
		entry := newCatalogueEntry(gen.CatalogueEntryKind_CATALOGUE_ENTRY_KIND_MODULE, grant.Prefix, now)
		entry.PublisherValue = &gen.CatalogueEntry_PublisherGap{PublisherGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueModulePublisher)}
		moduleEntries = append(moduleEntries, &PlatformCatalogueEntry{Entry: entry})
	}

	solutions := make(map[string]*PlatformCatalogueEntry)
	solution := func(name string) *PlatformCatalogueEntry {
		if existing, ok := solutions[name]; ok {
			return existing
		}
		entry := newCatalogueEntry(gen.CatalogueEntryKind_CATALOGUE_ENTRY_KIND_SOLUTION, name, now)
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
func newCatalogueEntry(kind gen.CatalogueEntryKind, name string, now time.Time) *gen.CatalogueEntry {
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
	judgeCatalogueObserved(entry.Authorized, entry.Observed, nil, now)
	return entry
}

// judgeCatalogueObserved sets the observed verdict from the authorized and
// observed states the entry already carries. It judges what runs against what
// is authorized, never against what is declared.
//
// The asymmetry here is deliberate. An affirmative verdict is constructible only
// from a complete, readable approval and a complete observation whose age is
// known and current, because an operator reads the green and stops looking. A
// verdict that reports trouble is never withheld for want of freshness: a stale
// report of something running unauthorized is still worth seeing.
//
//   - no observation: NOT_OBSERVED, never a match;
//   - an observation with a known absence of authorization: RUNNING_UNAUTHORIZED;
//   - an observation, but no authorization this host can read: NOT_RECORDED;
//   - an approval there is nothing to judge against — no build incarnation, an
//     inventory this host does not hold or the contract refuses, or a member the
//     inventory approves no container for: NOT_RECORDED. A defect in the
//     evidence is not a statement about what runs, so it is never DIFFERS;
//   - an observation missing an approved container: NOT_OBSERVED, because an
//     incomplete observation is never counted as approved execution;
//   - the approved execution exactly, but an observation whose age is unknown or
//     past catalogueObservationValidity: NOT_OBSERVED. A match this host cannot
//     date is not a match now;
//   - the approved execution exactly, currently observed: RUNNING_AUTHORIZED;
//   - anything else: RUNNING_DIFFERS.
//
// The approved execution is read from the approved inventory through the
// deployment contract, keyed by the inventory's digest in inventories.
func judgeCatalogueObserved(authorized *gen.CatalogueAuthorized, observed *gen.CatalogueObserved, inventories map[string][]byte, now time.Time) {
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
	// This host assigns the incarnation, starting at one, so zero is an approval
	// that carries none rather than one approving incarnation zero. Comparing an
	// observation against it would make any observation that also carries none
	// into a match, which is the whole class of false green this guard closes.
	if approval.GetBuildIncarnation() == 0 {
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
			"The approval carries no build incarnation, so there is nothing for an observed incarnation to match.")
		return
	}
	approved, err := approvedContainerImageDigests(inventories[approval.GetInventoryDigest()], approval.GetInventoryDigest(), approval.GetMemberBinding())
	if err != nil {
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, "The approved inventory cannot be read: "+err.Error())
		return
	}
	// Unreachable through an inventory the contract accepts: it refuses a
	// workload with no application container (CONTAINER_REQUIRED), which
	// TestExecutionInventoryContractRequiresAContainer pins. The guard is here
	// because the day that changes, an approved member with no container would
	// match an observation with none — the empty-equals-empty green this whole
	// function exists to refuse.
	if len(approved) == 0 {
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED,
			"The approved inventory approves no container for member "+approval.GetMemberBinding()+", so nothing observed can match it.")
		return
	}
	observedDigests := running.GetContainerImageDigests()
	for _, key := range slices.Sorted(maps.Keys(approved)) {
		if _, ok := observedDigests[key]; !ok {
			gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, "The observation is incomplete: approved container "+key+" was not observed.")
			return
		}
	}
	if !maps.Equal(approved, observedDigests) || running.GetBuildIncarnation() != approval.GetBuildIncarnation() {
		verdict(gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_DIFFERS)
		return
	}
	if reason := catalogueObservationNotCurrent(observed, now); reason != "" {
		gap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, reason)
		return
	}
	verdict(gen.CatalogueObservedVerdict_CATALOGUE_OBSERVED_VERDICT_RUNNING_AUTHORIZED)
}

// catalogueObservationNotCurrent says why an observation cannot speak for what
// runs now, or "" when it can. It is the one definition of observation currency:
// the verdict's author and the read boundary that enforces the invariant both
// read it here, so neither can drift from the other.
func catalogueObservationNotCurrent(observed *gen.CatalogueObserved, now time.Time) string {
	at := observed.GetObservedAt()
	if at == nil {
		return "The observation carries no time, so this host cannot tell whether it describes what runs now."
	}
	switch age := now.Sub(at.AsTime()); {
	case age > catalogueObservationValidity:
		return fmt.Sprintf("The observation is %s old, past the %s this host treats as current.",
			age.Round(time.Second), catalogueObservationValidity)
	case age < -catalogueObservationSkew:
		return fmt.Sprintf("The observation is stamped %s ahead of this host's clock, past the %s of skew it tolerates.",
			(-age).Round(time.Second), catalogueObservationSkew)
	}
	return ""
}

// approvedContainerImageDigests reads one member's approved containers out of
// an approved inventory, as "<workload id>/<container name>" → image manifest
// digest. Everything comes from the deployment contract: Check accepts the
// inventory's intrinsic form, its digest must be the one approval signed, and
// the containers are the contract's own projection, selected by member and
// workload name (Projections) — never paired by position, and never a
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
	out := make(map[string]string)
	found := false
	for _, projection := range checked.Projections() {
		if projection.MemberBinding != member {
			continue
		}
		found = true
		for name, reference := range projection.Row.Images {
			_, imageDigest, ok := strings.Cut(reference, "@")
			if !ok {
				return nil, fmt.Errorf("container %s/%s has no image digest", projection.Workload, name)
			}
			out[projection.Workload+"/"+name] = imageDigest
		}
	}
	if !found {
		return nil, fmt.Errorf("it has no member %q", member)
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
