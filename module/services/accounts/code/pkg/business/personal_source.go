package business

import (
	"context"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A personal source is an `api` source connected with the OAuth 2.0
// authorization-code grant. It reads with its connecting person's own sign-in,
// and only that person reads or calls it: its owner is PersonalOwnerUserID,
// its items' readers are the owner alone (connector.ApplySourcePolicy), and
// every call on it is refused to anyone else (sourceOperationAccess). It
// administers nothing of the organization and widens no one's reach, so a
// member connects one of their own, and declares its operations, without the
// organization administrator role.
//
// What they need instead is the one permission every call on it already
// needs: datasource:invoke on the scope node it is placed under, held by
// whoever decides for the session the person calls from — the person, or the
// team a cast acts as and nothing of the person's own. That keeps the
// administrator the one who decides where callable sources may be used, by
// granting it, and refuses a source its owner could never call. The node must
// already exist: minting a collection changes the organization's tree, which
// stays an administrator's.
//
// An organization's own source, and one a solution governs, are unchanged.

// SourceBoundaryHolder is whose datasource grants decide a call on a source,
// for the session a person calls from: the person, or the team a cast acts as
// and nothing of the person's own. It decides placing a source of their own
// under a boundary, changing it there, and every read of and call on a source
// (sourceOperationAccess).
type SourceBoundaryHolder struct {
	SubjectID   string
	SubjectKind gen.SubjectKind
}

// SourceHolderFor is the holder for person's session: the team it is cast
// onto when castTeam names one, the person otherwise. A role view names no
// team, so it is the person, as it always was.
func SourceHolderFor(person, castTeam string) SourceBoundaryHolder {
	if castTeam != "" {
		return SourceBoundaryHolder{SubjectID: castTeam, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM}
	}
	return SourceBoundaryHolder{SubjectID: person, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL}
}

// Cast reports whether the holder is the team a cast acts as, which holds no
// organization role and nothing else of its person's.
func (h SourceBoundaryHolder) Cast() bool { return h.SubjectKind == gen.SubjectKind_SUBJECT_KIND_TEAM }

// lacks words what holder does not hold, naming the team a cast acts as.
func (h SourceBoundaryHolder) lacks(action, nodeID string) string {
	who := "you do"
	if h.Cast() {
		who = fmt.Sprintf("team %s, which this session acts as, does", h.SubjectID)
	}
	return fmt.Sprintf("%s not hold datasource:%s on scope node %s", who, action, nodeID)
}

// holdsTx reports whether holder holds datasource:action on nodeID now, and
// nothing else: a team's grant query matches that team alone, with no grant of
// a person's and no other team's. It runs inside the caller's organization
// transaction.
func (s *Service) holdsTx(ctx context.Context, orgID string, holder SourceBoundaryHolder, action, nodeID string) (bool, error) {
	allowed, err := s.store.CanReadScopeNode(ctx, orgID, holder.SubjectID, holder.SubjectKind, "datasource", action, nodeID)
	if err != nil {
		return false, status.Error(codes.Unavailable, "source authority unavailable")
	}
	return allowed, nil
}

// personalSourceBoundaryAction is the permission a holder needs on a personal
// source's boundary: the one sourceOperationAccess requires of every call.
const personalSourceBoundaryAction = "invoke"

// connectsPersonalSource reports whether the input asks for a personal source:
// an api source whose credential is the person's own authorization-code grant.
func (in AddSourceInput) connectsPersonalSource() bool {
	return in.Provider == DatasourceProviderAPI && in.API != nil && in.API.OAuth2 != nil &&
		in.API.CredentialKind == APICredentialKindOAuth2 && in.API.OAuth2.Grant == OAuth2AuthorizationCode
}

// admitPersonalConnect refuses, before anything is sealed or stored, a connect
// a member makes as their own that is not a personal source on an existing node.
func admitPersonalConnect(input AddSourceInput) error {
	if input.PersonalTo == nil {
		return nil
	}
	if input.Governance != nil || !input.connectsPersonalSource() {
		return status.Error(codes.PermissionDenied,
			"requires organization administrator or owner role: only a source of your own — an api source connected with the OAuth 2.0 authorization-code grant — is connected without it")
	}
	if strings.TrimSpace(input.BoundaryNodeID) == "" {
		return status.Error(codes.PermissionDenied,
			"a source of your own is placed under an existing scope node (boundary_node_id): creating a collection for it is an organization administrator's")
	}
	return nil
}

// requireHoldsSourceBoundaryTx refuses unless holder holds datasource:invoke on
// nodeID now. It runs inside the caller's organization transaction, so the
// grant it reads is the one the write commits against.
func (s *Service) requireHoldsSourceBoundaryTx(ctx context.Context, orgID string, holder SourceBoundaryHolder, nodeID string) error {
	allowed, err := s.holdsTx(ctx, orgID, holder, personalSourceBoundaryAction, nodeID)
	if err != nil || allowed {
		return err
	}
	return status.Errorf(codes.PermissionDenied,
		"%s, which every call on a source of your own placed there needs: an organization administrator grants it",
		holder.lacks(personalSourceBoundaryAction, nodeID))
}

// RequireHoldsSourceBoundary refuses unless holder holds datasource:invoke on
// nodeID now, in its own organization transaction: the check
// requireHoldsSourceBoundaryTx makes, for a caller that writes nothing.
func (s *Service) RequireHoldsSourceBoundary(ctx context.Context, orgID string, holder SourceBoundaryHolder, nodeID string) error {
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.requireHoldsSourceBoundaryTx(ctx, orgID, holder, nodeID)
	})
}

// AuthorizeOwnSourceRead refuses a cast a personal source of its person's own
// placed where the team it acts as does not hold datasource:read: the person's
// ownership is something of their own, which a cast does not hold. A person's
// own session, and a role view, read their own source as before.
func (s *Service) AuthorizeOwnSourceRead(ctx context.Context, holder SourceBoundaryHolder, source *DatasourceSource) error {
	if !holder.Cast() || source == nil || source.PersonalOwnerUserID == "" {
		return nil
	}
	var allowed bool
	err := s.store.WithOrgTx(ctx, source.OrgID, func(ctx context.Context) error {
		var err error
		allowed, err = s.holdsTx(ctx, source.OrgID, holder, "read", source.BoundaryNodeID)
		return err
	})
	if err != nil || allowed {
		return err
	}
	return status.Error(codes.PermissionDenied, holder.lacks("read", source.BoundaryNodeID))
}

// DeclareOwnSourceOperations replaces the operations declared on the caller's
// own personal source, decided by holder's datasource:invoke on its boundary
// and never by an organization role. Any other source is refused here: an
// organization's own is its administrators' (DeclareSourceOperations), and a
// governed one its governing scope's.
func (s *Service) DeclareOwnSourceOperations(ctx context.Context, holder SourceBoundaryHolder, actor, org, source string, declarations []operations.Declaration) ([]operations.Declaration, error) {
	return s.declareSourceOperations(ctx, actor, org, source, declarations, sourceDeclarer{
		member: func(membership *gen.OrgMembership) error {
			if membership == nil {
				return status.Error(codes.PermissionDenied, "organization membership required")
			}
			return nil
		},
		// An absent source and another member's are refused alike, so a
		// member learns nothing of which personal sources exist.
		source: func(ctx context.Context, src *DatasourceSource) error {
			if src == nil || src.PersonalOwnerUserID == "" || src.PersonalOwnerUserID != actor {
				return status.Error(codes.PermissionDenied, "source is not a personal source of yours")
			}
			return s.requireHoldsSourceBoundaryTx(ctx, org, holder, src.BoundaryNodeID)
		},
	})
}

// DatasourceLinkSource is the source a link state was begun for: "" when the
// state names none (a provider-account link) or this host did not sign it, or
// it has expired.
func (s *Service) DatasourceLinkSource(state string) string {
	claims, err := s.verifyDatasourceLinkState(state, time.Now().UTC())
	if err != nil {
		return ""
	}
	return claims.SourceID
}
