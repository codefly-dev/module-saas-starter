package connector

import (
	"context"
	"sort"

	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

// Clause 2: an item's readers, in host terms. The rules below are the owner's
// (26 September 2026) and every connector reaches them through Translate or the
// constructors here, never by building a readers value of its own:
//
//   - a provider account maps to a host user only through an explicit account
//     link, never by email;
//   - a provider group maps to a host team only through an administrator's
//     explicit binding; an unbound group grants nothing;
//   - a guest outside the tenant is dropped and counted;
//   - "anyone with the link" makes the item UNTRANSLATABLE (admin-only);
//   - "anyone in the domain" grants the boundary's readers only when that
//     domain is verified to the tenant, and is dropped otherwise;
//   - a personal source is readable only by the person who connected it.
//
// UNTRANSLATABLE, and UNSPECIFIED, mean the boundary's administrators only.

// PrincipalKind is the kind of one provider access-list entry.
type PrincipalKind string

const (
	PrincipalAccount        PrincipalKind = "account"
	PrincipalGroup          PrincipalKind = "group"
	PrincipalGuest          PrincipalKind = "guest"
	PrincipalAnyoneWithLink PrincipalKind = "anyone_with_link"
	PrincipalDomain         PrincipalKind = "domain"
)

// ProviderPrincipal is one entry of a provider's access list. ID is the
// provider account or group id, or the domain for PrincipalDomain.
type ProviderPrincipal struct {
	Kind PrincipalKind
	ID   string
}

// ProviderACL is one item's access list as the provider reports it.
type ProviderACL struct {
	Entries []ProviderPrincipal
	// Version is the provider's version of the list, when it has one.
	Version string
}

// Directory answers the host-side questions translation needs, in bulk. The
// host owns every answer: account links, group bindings and verified domains.
type Directory interface {
	// LinkedUsers maps provider account ids to host user ids through explicit
	// account links; an unlinked account is absent from the result.
	LinkedUsers(ctx context.Context, connector string, accountIDs []string) (map[string]string, error)
	// BoundTeams maps provider group ids to host team ids through an
	// administrator's explicit binding; an unbound group is absent.
	BoundTeams(ctx context.Context, connector string, groupIDs []string) (map[string]string, error)
	// VerifiedDomains reports which of the domains are verified to the tenant.
	VerifiedDomains(ctx context.Context, orgID string, domains []string) (map[string]bool, error)
}

// SourceScoped is the readers of an item whose provider has no per-item access
// list: whoever may read the source's boundary.
func SourceScoped() *accountsv1.DatasourceItemReaders {
	return &accountsv1.DatasourceItemReaders{
		Basis:           accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED,
		BoundaryReaders: true,
	}
}

// Untranslatable is the readers of an item whose access list the host cannot
// honour: the boundary's administrators only.
func Untranslatable(unmapped int32, aclVersion string) *accountsv1.DatasourceItemReaders {
	return &accountsv1.DatasourceItemReaders{
		Basis:         accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_UNTRANSLATABLE,
		UnmappedCount: unmapped,
		AclVersion:    aclVersion,
	}
}

// Personal is the readers of every item of a personal source: its owner only.
func Personal(ownerUserID string) *accountsv1.DatasourceItemReaders {
	return &accountsv1.DatasourceItemReaders{
		Basis:   accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_TRANSLATED,
		UserIds: []string{ownerUserID},
	}
}

// ApplySourcePolicy is the last word on an item's readers: a personal source's
// item is readable only by its owner, whatever the connector translated.
func ApplySourcePolicy(src Source, readers *accountsv1.DatasourceItemReaders) *accountsv1.DatasourceItemReaders {
	if src.PersonalOwnerUserID != "" {
		return Personal(src.PersonalOwnerUserID)
	}
	return readers
}

// Translate maps one provider access list to host readers under the rules
// above. An access list that grants nobody the host can name is
// UNTRANSLATABLE; so is any list containing "anyone with the link".
func Translate(ctx context.Context, dir Directory, connectorKey, orgID string, acl ProviderACL) (*accountsv1.DatasourceItemReaders, error) {
	var accounts, groups, domains []string
	unmapped := int32(0)
	for _, e := range acl.Entries {
		switch e.Kind {
		case PrincipalAnyoneWithLink:
			// Settled question 4: a link share is never widened to anyone.
			return Untranslatable(countDropped(acl.Entries), acl.Version), nil
		case PrincipalAccount:
			accounts = append(accounts, e.ID)
		case PrincipalGroup:
			groups = append(groups, e.ID)
		case PrincipalDomain:
			domains = append(domains, e.ID)
		default: // guests, and any kind this host does not know
			unmapped++
		}
	}
	users, err := lookup(ctx, accounts, func(ids []string) (map[string]string, error) {
		return dir.LinkedUsers(ctx, connectorKey, ids)
	})
	if err != nil {
		return nil, err
	}
	teams, err := lookup(ctx, groups, func(ids []string) (map[string]string, error) {
		return dir.BoundTeams(ctx, connectorKey, ids)
	})
	if err != nil {
		return nil, err
	}
	verified := map[string]bool{}
	if len(domains) > 0 {
		if verified, err = dir.VerifiedDomains(ctx, orgID, domains); err != nil {
			return nil, err
		}
	}
	out := &accountsv1.DatasourceItemReaders{
		Basis:      accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_TRANSLATED,
		AclVersion: acl.Version,
	}
	userSet, teamSet := map[string]bool{}, map[string]bool{}
	for _, id := range accounts {
		if u, ok := users[id]; ok && u != "" {
			userSet[u] = true
		} else {
			unmapped++
		}
	}
	for _, id := range groups {
		if t, ok := teams[id]; ok && t != "" {
			teamSet[t] = true
		} else {
			unmapped++
		}
	}
	for _, d := range domains {
		if verified[d] {
			out.BoundaryReaders = true
		} else {
			unmapped++
		}
	}
	out.UserIds, out.GroupIds = sortedKeys(userSet), sortedKeys(teamSet)
	out.UnmappedCount = unmapped
	if len(out.UserIds) == 0 && len(out.GroupIds) == 0 && !out.BoundaryReaders {
		return Untranslatable(unmapped, acl.Version), nil
	}
	return out, nil
}

// ReadersError reports why a readers value breaks the envelope, or nil.
func ReadersError(r *accountsv1.DatasourceItemReaders) string {
	if r == nil {
		return "readers are missing"
	}
	switch r.GetBasis() {
	case accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED:
		if !r.GetBoundaryReaders() || len(r.GetUserIds()) > 0 || len(r.GetGroupIds()) > 0 {
			return "a source-scoped item grants the boundary's readers and nobody else"
		}
	case accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_UNTRANSLATABLE:
		if r.GetBoundaryReaders() || len(r.GetUserIds()) > 0 || len(r.GetGroupIds()) > 0 {
			return "an untranslatable item grants nobody but the boundary's administrators"
		}
	case accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_TRANSLATED:
		if !r.GetBoundaryReaders() && len(r.GetUserIds()) == 0 && len(r.GetGroupIds()) == 0 {
			return "a translated item that grants nobody must be untranslatable"
		}
	default:
		return "readers carry no basis"
	}
	if r.GetUnmappedCount() < 0 {
		return "unmapped count is negative"
	}
	return ""
}

func lookup(ctx context.Context, ids []string, fn func([]string) (map[string]string, error)) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fn(ids)
}

func countDropped(entries []ProviderPrincipal) int32 {
	n := int32(0)
	for _, e := range entries {
		if e.Kind != PrincipalAnyoneWithLink {
			n++
		}
	}
	return n
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
