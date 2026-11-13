package business

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A connect a member makes as their own is admitted only for a personal source
// placed under an existing node, before anything is sealed or stored.
func TestAMemberConnectsOnlyAPersonalSourceUnderAnExistingNode(t *testing.T) {
	holder := &SourceBoundaryHolder{SubjectID: "member", SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL}
	personal := func() AddSourceInput {
		return AddSourceInput{Provider: DatasourceProviderAPI, BoundaryNodeID: "node", PersonalTo: holder,
			API: &APIDatasourceConfig{CredentialKind: APICredentialKindOAuth2, OAuth2: &APIOAuth2Config{Grant: OAuth2AuthorizationCode}}}
	}
	require.NoError(t, admitPersonalConnect(personal()))
	adminOrGoverned := personal()
	adminOrGoverned.PersonalTo = nil
	adminOrGoverned.BoundaryNodeID, adminOrGoverned.CollectionLabel = "", "a label"
	require.NoError(t, admitPersonalConnect(adminOrGoverned), "a connect decided otherwise is not this rule's")

	for name, change := range map[string]func(*AddSourceInput){
		"client credentials": func(in *AddSourceInput) { in.API.OAuth2.Grant = OAuth2ClientCredentials },
		"a refresh token":    func(in *AddSourceInput) { in.API.OAuth2.Grant = OAuth2RefreshToken },
		"a bearer":           func(in *AddSourceInput) { in.API = &APIDatasourceConfig{CredentialKind: "bearer"} },
		"another provider":   func(in *AddSourceInput) { in.Provider, in.API = DatasourceProviderGitHub, nil },
		"governed":           func(in *AddSourceInput) { in.Governance = &SourceGovernance{InstallationID: "i", Kind: "k", Action: "write"} },
	} {
		in := personal()
		change(&in)
		err := admitPersonalConnect(in)
		require.Equal(t, codes.PermissionDenied, status.Code(err), name)
		require.Contains(t, status.Convert(err).Message(), "requires organization administrator or owner role", name)
	}
	labelled := personal()
	labelled.BoundaryNodeID, labelled.CollectionLabel = "", "a new collection"
	err := admitPersonalConnect(labelled)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "existing scope node")
}
