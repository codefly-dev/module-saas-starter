package accounts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	accounts "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go"
	v1 "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go/gen/saas/accounts/v1"
	rpc "github.com/codefly-dev/module-saas-starter/libraries/source-read-sdk/go/gen/saas/accounts/v1/accountsv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

type projection struct {
	rpc.UnimplementedModuleCapabilitiesServiceHandler
}

func (projection) ListReadableSourceCollections(_ context.Context, r *connect.Request[v1.ListReadableSourceCollectionsRequest]) (*connect.Response[v1.ListReadableSourceCollectionsResponse], error) {
	if r.Msg.GetPageToken() != "cursor" || r.Header().Get("X-Codefly-Work-Context") != "original-viewer" || r.Header().Get("X-Codefly-Internal-Token") != "perimeter" {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	return connect.NewResponse(&v1.ListReadableSourceCollectionsResponse{Collections: []*v1.ReadableSourceCollection{{SourceId: "source", BoundaryId: "collection", Origin: "github", Container: "acme/handbook", Paths: []string{"docs"}}}}), nil
}
func TestGeneratedSourceProjectionUsesInternalGRPC(t *testing.T) {
	_, handler := rpc.NewModuleCapabilitiesServiceHandler(projection{})
	// The production internal listener accepts HTTP/2 gRPC, not Connect HTTP.
	internal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(internal)
	server.Config.Protocols = new(http.Protocols)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	defer server.Close() //nolint:staticcheck // private listener uses h2c
	credentials := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("X-Codefly-Work-Context", "original-viewer")
			req.Header().Set("X-Codefly-Internal-Token", "perimeter")
			return next(ctx, req)
		}
	})
	client, err := accounts.NewInternal(server.URL, connect.WithInterceptors(credentials))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ModuleCapabilities().ListReadableSourceCollections(context.Background(), &v1.ListReadableSourceCollectionsRequest{PageToken: "cursor"})
	if err != nil || len(result.GetCollections()) != 1 || result.Collections[0].BoundaryId != "collection" {
		t.Fatalf("%v %v", result, err)
	}
}
func TestContractContainsOnlyRequiredDescriptors(t *testing.T) {
	raw, err := os.ReadFile("../contract/contract.binpb")
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	files := map[string]*descriptorpb.FileDescriptorProto{}
	for _, f := range set.File {
		files[f.GetName()] = f
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		f := files[name]
		if f == nil {
			t.Fatalf("missing dependency %s", name)
		}
		for _, dep := range f.Dependency {
			visit(dep)
		}
	}
	visit("saas/accounts/v1/module_capabilities.proto")
	if len(seen) != len(files) {
		t.Fatalf("%d unrelated descriptors shipped", len(files)-len(seen))
	}
	raw, err = os.ReadFile("../contract/catalog.codefly.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Endpoints []struct{ Services []struct{ Name string } }
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Endpoints) != 1 || len(catalog.Endpoints[0].Services) != 1 || catalog.Endpoints[0].Services[0].Name != "ModuleCapabilitiesService" {
		t.Fatal("unrelated service exposed")
	}
}
