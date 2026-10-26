package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	authorityv1connect "accounts/pkg/gen/host/authority/v1/authorityv1connect"
)

// Regression: removing a complete kind upstream must invalidate descendants.
func TestUpstreamKindRemovalEndsDescendant(t *testing.T) {
	f := newHopFixture(t, "tool.items:read", "example-records:read")
	journal := &bindingJournal{hops: map[string]business.ActorChainHopInput{}}
	f.issuer.journal = journal
	mux := http.NewServeMux()
	path, handler := authorityv1connect.NewAuthorityServiceHandler(&hostAuthorityConnectHandler{inner: f.server})
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := authorityv1connect.NewAuthorityServiceClient(server.Client(), server.URL)
	parent, _ := f.parent(t, "tools", hopScope("tool.items", []string{"read"}))
	issue := func(parent, credential, binding, audience string) string {
		out, err := client.Issue(context.Background(), connect.NewRequest(&authorityv1.IssueRequest{
			As: &authorityv1.IssueRequest_ParentToken{ParentToken: parent}, Actor: &authorityv1.WorkloadCredential{Credential: credential},
			OperationBindingId: binding, Audience: audience,
		}))
		require.NoError(t, err)
		return out.Msg.GetToken()
	}
	first := issue(parent, f.tool, "records", "documents")
	child := issue(first, f.docs, "items", "tools")
	check := func() error {
		_, err := client.Check(context.Background(), connect.NewRequest(&authorityv1.CheckRequest{Token: child, Audience: "tools"}))
		return err
	}
	require.NoError(t, check())
	// Missing provenance cannot turn a historical grant into an unrelated kind.
	recorded := journal.hops
	journal.hops = map[string]business.ActorChainHopInput{}
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(check()))
	journal.hops = recorded
	require.NoError(t, check())
	recompose(t, func(s string) string {
		s = strings.Replace(s, `"invoke_scopes":[{"resource_kind":"example-records","actions":["invoke","read"]}]`, `"invoke_scopes":[{"resource_kind":"other-records","actions":["read"]}]`, 1)
		return strings.Replace(s, `,"lookup_scopes":[{"resource_kind":"example-records","actions":["read"]}]`, ``, 1)
	})
	err := check()
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "descendant Check must refuse after its upstream binding drops example-records entirely; got %v", err)
	_, err = client.Issue(context.Background(), connect.NewRequest(&authorityv1.IssueRequest{
		As: &authorityv1.IssueRequest_ParentToken{ParentToken: child}, Actor: &authorityv1.WorkloadCredential{Credential: f.tool},
		OperationBindingId: "records", Audience: "documents",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "a stale descendant cannot issue another hop")
}
