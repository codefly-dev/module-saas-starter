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

// SourceBoundaryHolder is who decides, for the session a person calls from,
// whether a source of their own may be placed under, or changed at, a
// boundary: the person, or the team a cast acts as.
type SourceBoundaryHolder struct {
	SubjectID   string
	SubjectKind gen.SubjectKind
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
	allowed, err := s.store.CanReadScopeNode(ctx, orgID, holder.SubjectID, holder.SubjectKind, "datasource", personalSourceBoundaryAction, nodeID)
	if err != nil {
		return status.Error(codes.Unavailable, "source authority unavailable")
	}
	if allowed {
		return nil
	}
	who := "you do"
	if holder.SubjectKind == gen.SubjectKind_SUBJECT_KIND_TEAM {
		who = fmt.Sprintf("team %s, which this session acts as, does", holder.SubjectID)
	}
	return status.Errorf(codes.PermissionDenied,
		"%s not hold datasource:%s on scope node %s, which every call on a source of your own placed there needs: an organization administrator grants it",
		who, personalSourceBoundaryAction, nodeID)
}

// RequireHoldsSourceBoundary refuses unless holder holds datasource:invoke on
// nodeID now, in its own organization transaction: the check
// requireHoldsSourceBoundaryTx makes, for a caller that writes nothing.
func (s *Service) RequireHoldsSourceBoundary(ctx context.Context, orgID string, holder SourceBoundaryHolder, nodeID string) error {
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.requireHoldsSourceBoundaryTx(ctx, orgID, holder, nodeID)
	})
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
