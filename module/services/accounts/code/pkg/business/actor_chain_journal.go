package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// The actor-chain journal makes the on-behalf-of chain durable, linked, and
// revocable (RFC-0003). The attenuation mechanism already lives in the signed,
// short-lived Work Context token; what the token cannot provide on its own is a
// record that survives its own expiry. Each actor hop is content-addressed and
// appended to a hash-chained journal at issuance, so "agent X acted for Sarah
// under grant Z" is reconstructable after the token is gone.
//
// Three properties layer on top of the existing capability chain:
//
//   - Durable: every hop is persisted, hash-chained to its parent hop.
//   - Linked: a hop's journal id IS the token's per-hop delegation_id, and a hop
//     may reference the approval grant (delegation_grants) that authorized it.
//   - Revocable: each hop carries a per-hop revocation id; revoking an ancestor
//     kills every descendant that chains through it (Biscuit-style), layered on
//     the coarse authorization_revision epoch already sealed into the token.

// ActorChainHopInput is one on-behalf-of hop to append to the journal. ID is the
// value stamped as the token's per-hop delegation_id, so the ephemeral token and
// the durable row share one identifier. ParentDelegationID is the id of the hop
// this one narrows from (empty for the root actor hop). DelegationGrantID links
// the hop to the human-approved grant that authorized it, when one exists.
type ActorChainHopInput struct {
	ID                    string
	OrgID                 string
	TaskID                string
	SessionID             string
	OwnerPrincipalID      string
	ActorPrincipalID      string
	ActorKind             string
	ParentDelegationID    string
	DelegationGrantID     string
	GrantedScopes         []ActorChainScope
	AuthorizationRevision uint64
	HopIndex              int
}

// ActorChainScope is the durable projection of one granted work scope. It mirrors
// the token's WorkScopeV1 shape but stays free of the proto dependency so the
// content-hash canonicalization is owned here.
type ActorChainScope struct {
	ResourceKind string   `json:"resource_kind"`
	Actions      []string `json:"actions,omitempty"`
	ResourceIDs  []string `json:"resource_ids,omitempty"`
}

// ActorChainHop is a persisted journal row.
type ActorChainHop struct {
	ID                    string
	OrgID                 string
	TaskID                string
	SessionID             string
	OwnerPrincipalID      string
	ActorPrincipalID      string
	ActorKind             string
	ParentDelegationID    string
	DelegationGrantID     string
	GrantedScopes         []ActorChainScope
	AuthorizationRevision uint64
	RevocationID          string
	HopIndex              int
	PrevHash              string
	HopHash               string
}

// ActorChainJournal is the durable-store seam for the on-behalf-of chain. It is
// deliberately separate from the issuance and consumer authority seams so an
// Accounts deployment can wire durability independently, and so the issuer never
// gains a generic mutation oracle.
type ActorChainJournal interface {
	// AppendActorChainHop content-addresses the hop against its parent's stored
	// hash and appends it. It is idempotent on the hop id: re-issuing the same
	// hop returns the already-stored row rather than forking a duplicate.
	AppendActorChainHop(ctx context.Context, hop ActorChainHopInput) (*ActorChainHop, error)
	// IsActorChainHopRevoked reports whether the hop, or any ancestor it chains
	// through, has been revoked.
	IsActorChainHopRevoked(ctx context.Context, orgID, hopID string) (bool, error)
	// RevokeActorChainHop records a revocation for the hop's per-hop revocation
	// id, killing the hop and every descendant that chains through it.
	RevokeActorChainHop(ctx context.Context, orgID, hopID, revokedByPrincipalID, reason string) error
}

// HopContentHash is the content address of a hop: sha256 over the hop's immutable
// facts folded with the parent hop's hash. It excludes assigned metadata (the id,
// the revocation id, the grant link) so the address depends only on what the hop
// authorizes — two hops authorizing the same thing under the same parent hash to
// the same address, and any later edit to a stored field breaks the chain.
func HopContentHash(hop ActorChainHopInput, prevHash string) string {
	var b strings.Builder
	b.WriteString(hop.OrgID)
	b.WriteByte('\n')
	b.WriteString(hop.OwnerPrincipalID)
	b.WriteByte('\n')
	b.WriteString(hop.TaskID)
	b.WriteByte('\n')
	b.WriteString(hop.SessionID)
	b.WriteByte('\n')
	b.WriteString(hop.ActorPrincipalID)
	b.WriteByte('\n')
	b.WriteString(hop.ActorKind)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatUint(hop.AuthorizationRevision, 10))
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(hop.HopIndex))
	b.WriteByte('\n')
	b.WriteString(canonicalScopes(hop.GrantedScopes))
	b.WriteByte('\n')
	b.WriteString(prevHash)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// canonicalScopes renders scopes into a stable string independent of the caller's
// ordering: actions and resource ids are sorted within a scope, and scopes are
// sorted by their rendered form. Every element is length-prefixed so the encoding
// is injective — no combination of separators embedded in a scope value can forge
// a different scope set's rendering.
func canonicalScopes(scopes []ActorChainScope) string {
	rendered := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		actions := append([]string(nil), scope.Actions...)
		sort.Strings(actions)
		resourceIDs := append([]string(nil), scope.ResourceIDs...)
		sort.Strings(resourceIDs)
		var b strings.Builder
		writeLengthPrefixed(&b, scope.ResourceKind)
		writeLengthPrefixed(&b, strconv.Itoa(len(actions)))
		for _, action := range actions {
			writeLengthPrefixed(&b, action)
		}
		writeLengthPrefixed(&b, strconv.Itoa(len(resourceIDs)))
		for _, resourceID := range resourceIDs {
			writeLengthPrefixed(&b, resourceID)
		}
		rendered = append(rendered, b.String())
	}
	sort.Strings(rendered)
	var out strings.Builder
	writeLengthPrefixed(&out, strconv.Itoa(len(rendered)))
	for _, scope := range rendered {
		writeLengthPrefixed(&out, scope)
	}
	return out.String()
}

func writeLengthPrefixed(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

// RFC8693Subject is the RFC 8693 token-exchange view of an actor chain: the
// end-user subject with a nested act claim. Only the current (outermost) actor
// authorizes; nested act claims are provenance.
type RFC8693Subject struct {
	Sub string        `json:"sub"`
	Act *RFC8693Actor `json:"act,omitempty"`
}

// RFC8693Actor is one act claim. The most deeply nested act is the least recent
// actor; the outermost is the current actor (RFC 8693 §4.1).
type RFC8693Actor struct {
	Sub string        `json:"sub"`
	Act *RFC8693Actor `json:"act,omitempty"`
}

// ActorChainToRFC8693Subject maps an owner and an actor chain (ordered
// earliest→current, as the token carries it) to the nested act representation.
// With no actors the subject is the owner acting directly, so act is nil.
func ActorChainToRFC8693Subject(ownerSub string, actorSubs []string) RFC8693Subject {
	var act *RFC8693Actor
	for _, sub := range actorSubs {
		act = &RFC8693Actor{Sub: sub, Act: act}
	}
	return RFC8693Subject{Sub: ownerSub, Act: act}
}
