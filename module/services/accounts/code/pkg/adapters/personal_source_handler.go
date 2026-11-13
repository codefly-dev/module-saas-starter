package adapters

import (
	"context"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// personalSourceRequest reports whether a connect asks for a personal source:
// an api source whose credential is the person's own OAuth 2.0
// authorization-code grant, tied to no solution's installation. The host makes
// such a source personal to whoever connects it (business.connectsPersonalSource).
func personalSourceRequest(m *gen.AddSourceRequest) bool {
	api := m.GetApi()
	return m.GetGovernance() == nil &&
		m.GetProvider() == gen.DatasourceProvider_DATASOURCE_PROVIDER_API &&
		api.GetCredentialKind() == gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2 &&
		api.GetOauth2().GetGrant() == gen.ApiOAuth2Config_GRANT_AUTHORIZATION_CODE
}

// personalSourceHolder decides who answers for a personal source of the
// caller's own, in the session the call is made from (business.SourceBoundaryHolder):
//
//   - the person's own session, if they administer the organization: nobody
//     else, and administrator is true, so the call is decided as an
//     administrator's, as it always was;
//   - the person's own session otherwise: the person, a member;
//   - a cast: the team it acts as, and nothing of the person's own, whatever
//     role the person holds. The source is still the person's: a cast acts as
//     its team, but a sign-in is a person's;
//   - a role view: refused, as it is for every operation it cannot narrow — a
//     view names roles at installations, and a source's boundary is decided by
//     grants this host reads by subject, which a view does not narrow.
func personalSourceHolder(ctx context.Context, actorID, orgID string) (holder *business.SourceBoundaryHolder, administrator bool, err error) {
	view, err := roleViewOf(ctx)
	if err != nil {
		return nil, false, err
	}
	if narrows(view) && view.Team == "" {
		return nil, false, viewRefusal(view)
	}
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, false, err
	}
	if view.Team != "" {
		return &business.SourceBoundaryHolder{SubjectID: view.Team, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM}, false, nil
	}
	if requireOrgAdmin(ctx, actorID, orgID) == nil {
		return nil, true, nil
	}
	return &business.SourceBoundaryHolder{SubjectID: actorID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL}, false, nil
}

// ownPersonalSource is source id of orgID when it is a personal source of
// actorID's that no solution governs; nil otherwise, so the caller decides it
// as any other source.
func (h *datasourceConnectHandler) ownPersonalSource(ctx context.Context, actorID, orgID, id string) (*business.DatasourceSource, error) {
	source, governed, err := h.governedSource(ctx, orgID, id)
	if err != nil || source == nil || governed || source.PersonalOwnerUserID == "" || source.PersonalOwnerUserID != actorID {
		return nil, err
	}
	return source, nil
}

// refuseUnlessOwnPersonalSourceInView refuses a call a cast reached
// (castAsksForItsPersonsOwnSource) unless it is about a personal source of the
// person's own, of provider connector when one is named. A session that
// narrows nothing passes; it is decided as before.
func (h *datasourceConnectHandler) refuseUnlessOwnPersonalSourceInView(ctx context.Context, actorID, orgID, sourceID, connector string) error {
	view, err := roleViewOf(ctx)
	if err != nil || !narrows(view) {
		return err
	}
	if view.Team == "" || sourceID == "" {
		return viewRefusal(view)
	}
	own, err := h.ownPersonalSource(ctx, actorID, orgID, sourceID)
	if err != nil {
		return err
	}
	if own == nil || (connector != "" && own.Provider != connector) {
		return viewRefusal(view)
	}
	return nil
}
