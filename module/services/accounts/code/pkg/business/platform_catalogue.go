package business

import (
	"context"
	"slices"
	"strings"

	"github.com/codefly-dev/core/wool"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The platform Catalogue (issue #1020) is a projection over records this host
// already keeps — the durable solution registry, the installations table and the
// composition's module principal registry. It adds no store of its own.
//
// Some facts the Catalogue is specified to show have no record on this host
// yet. Each is reported as a CatalogueGap naming why, never as an empty value:
// an unrecorded generation is not generation 0, an unreported build size is not
// zero lines, and an unobserved deployment is not one that matches.

// CatalogueInstallationRecord is one active installation as the Catalogue reads
// it: the installation, its organization's name, the identifier of the agent
// principal it was installed as, and the team grants that reach inside it.
type CatalogueInstallationRecord struct {
	Installation    *gen.Installation
	OrgName         string
	AgentIdentifier string
	ExposedTeams    []*gen.CollectionReadGrant
}

// PlatformCatalogueEntry is one Catalogue row. Registration is kept as the
// business record so the transport projects it through the same mapping every
// other registry response uses; Entry carries everything else.
type PlatformCatalogueEntry struct {
	Entry        *gen.CatalogueEntry
	Registration *SolutionRegistration
}

// PlatformCatalogue is the whole Catalogue at one registry revision.
type PlatformCatalogue struct {
	Entries          []*PlatformCatalogueEntry
	RegistryRevision int64
}

const (
	catalogueNoPresenceDetail = "This host holds no applied presence document for this entry, which is what records it."
	catalogueNotObserved      = "This host reads no observed cluster state, so what runs is unknown — never assumed to match."
	catalogueNoBuildSize      = "Build size is the presence document's build_size section (codefly-dev/core#708), computed at build by the CLI; this host holds no presence document for this entry."
	catalogueModulePublisher  = "A composed module is known by its principal prefix alone; the composition records no publisher."
	catalogueUnregistered     = "No registration exists under this identifier; it is named only by its installations."
	catalogueNoRevision       = "Installations are not pinned to a presence revision on this host."
	catalogueNoAgentRelease   = "The installation's agent principal carries no publisher/name:version identifier."
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

// newCatalogueEntry is an entry whose presence-borne facts are all gaps: no
// record on this host carries a declared release, build digest, generation,
// observed execution or build size yet. When one does, the entry reads it here
// and the gap gives way to the value.
func newCatalogueEntry(kind gen.CatalogueEntryKind, name string) *gen.CatalogueEntry {
	notRecorded := gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED
	entry := &gen.CatalogueEntry{
		Kind:                 kind,
		Name:                 name,
		DeclaredReleaseValue: &gen.CatalogueEntry_DeclaredReleaseGap{DeclaredReleaseGap: catalogueGap(notRecorded, catalogueNoPresenceDetail)},
		BuildDigestValue:     &gen.CatalogueEntry_BuildDigestGap{BuildDigestGap: catalogueGap(notRecorded, catalogueNoPresenceDetail)},
		GenerationValue:      &gen.CatalogueEntry_GenerationGap{GenerationGap: catalogueGap(notRecorded, catalogueNoPresenceDetail)},
		BuildSizeValue:       &gen.CatalogueEntry_BuildSizeGap{BuildSizeGap: catalogueGap(notRecorded, catalogueNoBuildSize)},
	}
	setCatalogueRunning(entry, nil, nil)
	return entry
}

// setCatalogueRunning states what runs beside what was declared. A verdict is
// given only when both are known: without an observation the answer is
// NOT_OBSERVED, and without a declaration there is nothing to compare an
// observation with. Neither is ever reported as a match.
func setCatalogueRunning(entry *gen.CatalogueEntry, declared, observed *gen.CatalogueExecution) {
	switch {
	case observed == nil:
		entry.RunningValue = &gen.CatalogueEntry_RunningGap{RunningGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_OBSERVED, catalogueNotObserved)}
	case declared == nil:
		entry.RunningValue = &gen.CatalogueEntry_RunningGap{RunningGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueNoPresenceDetail)}
	default:
		verdict := gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_DIFFERS
		if declared.GetImageDigest() != "" && declared.GetImageDigest() == observed.GetImageDigest() &&
			declared.GetBuildIncarnation() == observed.GetBuildIncarnation() {
			verdict = gen.CatalogueRunningVerdict_CATALOGUE_RUNNING_VERDICT_MATCHES
		}
		entry.RunningValue = &gen.CatalogueEntry_Running{Running: &gen.CatalogueRunning{Declared: declared, Observed: observed, Verdict: verdict}}
	}
}

func catalogueInstallation(record *CatalogueInstallationRecord) *gen.CatalogueInstallation {
	out := &gen.CatalogueInstallation{
		Installation: record.Installation,
		OrgName:      record.OrgName,
		RevisionValue: &gen.CatalogueInstallation_RevisionGap{
			RevisionGap: catalogueGap(gen.CatalogueGapReason_CATALOGUE_GAP_REASON_NOT_RECORDED, catalogueNoRevision),
		},
		ExposedTeams: record.ExposedTeams,
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
