package accounts_test

import (
	"connectrpc.com/connect"
	"context"
	accounts "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go"
	v1 "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go/gen/saas/accounts/v1"
	rpc "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go/gen/saas/accounts/v1/accountsv1connect"
	"net/http"
	"net/http/httptest"
	"testing"
)

type gateway struct{ *httptest.Server }

func (g gateway) BaseURL() string          { return g.URL }
func (g gateway) HTTPClient() *http.Client { return g.Client() }

type projection struct {
	rpc.UnimplementedModuleCapabilitiesServiceHandler
}

func (projection) ListReadableSourceCollections(_ context.Context, r *connect.Request[v1.ListReadableSourceCollectionsRequest]) (*connect.Response[v1.ListReadableSourceCollectionsResponse], error) {
	if r.Msg.GetPageToken() != "cursor" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	return connect.NewResponse(&v1.ListReadableSourceCollectionsResponse{Collections: []*v1.ReadableSourceCollection{{SourceId: "source", BoundaryId: "collection", Origin: "github", Container: "acme/handbook", Paths: []string{"docs"}}}}), nil
}
func TestGeneratedSourceProjectionFacade(t *testing.T) {
	_, h := rpc.NewModuleCapabilitiesServiceHandler(projection{})
	server := httptest.NewServer(h)
	defer server.Close()
	result, err := accounts.New(gateway{server}).ModuleCapabilities().ListReadableSourceCollections(context.Background(), &v1.ListReadableSourceCollectionsRequest{PageToken: "cursor"})
	if err != nil || len(result.GetCollections()) != 1 || result.Collections[0].BoundaryId != "collection" {
		t.Fatalf("%v %v", result, err)
	}
}
