package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"
	"github.com/codefly-dev/authority/libraries/authority-client/go/stamp"
	hop "github.com/codefly-dev/authority/libraries/hop/go"
	"github.com/codefly-dev/authority/libraries/hop/go/hoptest"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	core "github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// THE GATEWAY AS THE HOP, through the hop library's own front door, in front
// of a module that verifies every call with the library's Verifier against the
// keys the host publishes. accounts is a double that behaves as accounts does
// on the authority interface: Issue signs a genuine Work Context, Check answers
// with its claims, RegisterAdmissionKey publishes the key the gateway proves,
// and Keys publishes it among the admission keys. What these pin:
//
//   - a call the gateway signs is admitted by the module, as the person;
//   - the admission it signed cannot be replayed at the module;
//   - a call that did not come through the gateway is refused.

// hostAuthority is accounts on the authority interface, for these tests.
type hostAuthority struct {
	signer *hoptest.Authority
	t      testing.TB

	mu         sync.Mutex
	admission  []*authorityv1.PublicKey
	registered int
}

func (a *hostAuthority) Issue(_ context.Context, in *authorityv1.IssueRequest, _ ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	if in.GetPerson() == nil {
		return nil, status.Error(codes.InvalidArgument, "only a person is issued here")
	}
	token := a.signer.Issue(a.t, in.GetAudience(), &basev0.WorkScopeV1{ResourceKind: "documents", Actions: []string{"read"}})
	decoded, err := core.Decode(token)
	if err != nil {
		return nil, err
	}
	return &authorityv1.IssueResponse{Token: token, ExpiresAtUnix: decoded.Context().GetExpiresAtUnix()}, nil
}

func (a *hostAuthority) Check(_ context.Context, in *authorityv1.CheckRequest, _ ...grpc.CallOption) (*authorityv1.CheckResponse, error) {
	decoded, err := core.Decode(in.GetToken())
	if err != nil || decoded.Context().GetAudience() != in.GetAudience() {
		return nil, status.Error(codes.PermissionDenied, "work context: not addressed to "+in.GetAudience())
	}
	wire, err := proto.Marshal(decoded.Context())
	if err != nil {
		return nil, err
	}
	wc := &authorityv1.WorkContextV1{}
	if err := proto.Unmarshal(wire, wc); err != nil {
		return nil, err
	}
	return &authorityv1.CheckResponse{Context: wc}, nil
}

// RegisterAdmissionKey publishes the key whose proof verifies, as accounts
// does (stamp.CheckRegistration), and nothing else.
func (a *hostAuthority) RegisterAdmissionKey(_ context.Context, in *authorityv1.RegisterAdmissionKeyRequest, _ ...grpc.CallOption) (*authorityv1.RegisterAdmissionKeyResponse, error) {
	public := ed25519.PublicKey(in.GetPublicKey())
	if _, err := stamp.CheckRegistration(in.GetProof(), public, in.GetActor().GetCredential(), "accounts", time.Now()); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	key := &authorityv1.PublicKey{KeyId: stamp.KeyID(public), Algorithm: core.Algorithm, PublicKey: public, Purpose: stamp.Purpose}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registered++
	if len(a.admission) == 0 {
		a.admission = append(a.admission, key)
	}
	return &authorityv1.RegisterAdmissionKeyResponse{Key: key, ExpiresAtUnix: time.Now().Add(2 * time.Minute).Unix()}, nil
}

// Keys publishes the Work Context key and the registered admission keys.
func (a *hostAuthority) Keys(ctx context.Context, in *authorityv1.KeysRequest, opts ...grpc.CallOption) (*authorityv1.KeysResponse, error) {
	keys, err := a.signer.Keys(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(keys).(*authorityv1.KeysResponse)
	a.mu.Lock()
	defer a.mu.Unlock()
	out.AdmissionKeys = append(out.AdmissionKeys, a.admission...)
	return out, nil
}

// workloadCredential names the authority it is presented to, as a projected
// token does; the double attests nothing else.
func workloadCredential() (string, error) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"accounts"}`))
	return "header." + claims + ".signature", nil
}

// verifyingModule is a module's Connect listener: every call is admitted by
// the library's Verifier, against the host's published keys, before it runs.
type verifyingModule struct {
	verifier *hop.Verifier

	mu       sync.Mutex
	reached  int
	arrived  http.Header
	identity hop.Identity
}

func (m *verifyingModule) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	md := metadata.MD{}
	for key, values := range r.Header {
		md[strings.ToLower(key)] = values
	}
	id, done, err := m.verifier.Admit(r.Context(), md, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	defer done()
	_, _ = io.Copy(io.Discard, r.Body)
	m.mu.Lock()
	m.reached++
	m.arrived = r.Header.Clone()
	m.identity = id
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"owner": id.Owner, "tenant": id.Tenant})
}

func hopHarness(t *testing.T) (*Gateway, *hostAuthority, *verifyingModule, *httptest.Server, string) {
	t.Helper()
	// The module's replay store is process-local, which only the local run may
	// use, and the routes address loopback, which only the local run dials in
	// plaintext; both are the module's and the composition's concern, not the
	// gateway's.
	t.Setenv("CODEFLY__ENVIRONMENT", "local")
	gw, _, _, priv := newGatewayHarness(t)
	accounts := &hostAuthority{signer: hoptest.New(t), t: t}
	module := &verifyingModule{}
	moduleServer := httptest.NewServer(module)
	t.Cleanup(moduleServer.Close)

	routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{{
		Audience: "documents", Service: "documents", Endpoint: "connect", Transport: "connect",
		Address: moduleServer.URL, Package: "documents", Version: "0.1.0", Digest: "sha256:d",
		Methods: []string{listCollection},
	}}}
	raw, err := json.Marshal(routes)
	require.NoError(t, err)
	role, front, err := newHopRole(t.Context(), accounts, workloadCredential, []string{"documents"}, string(raw), routes, "")
	require.NoError(t, err)
	t.Cleanup(front.Close)
	require.Equal(t, 1, accounts.registered, "the gateway registers its admission key before it serves")
	// The module reads the host's keys as it boots, once the gateway's key is
	// published.
	module.verifier, err = hop.NewVerifier(t.Context(), accounts, "documents", hoptest.Replay())
	require.NoError(t, err)
	gw.hop = role
	return gw, accounts, module, moduleServer, signValidToken(t, priv)
}

func listCollectionRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	return req
}

// A browser's call through the gateway is admitted by the module's own
// Verifier, as the person the authority issued the Work Context for, signed
// with the key the gateway registered with the host.
func TestHopTheGatewaySignedCallIsAdmittedByTheModule(t *testing.T) {
	gw, _, module, _, bearer := hopHarness(t)

	req := listCollectionRequest(listCollection)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, module.reached)
	require.Equal(t, core.FixturePrincipal, module.identity.Owner, "the module admitted the call as the person")
	require.NotEmpty(t, module.arrived.Get("X-Codefly-Hop-Proof"), "the gateway signed the admission")
	require.Empty(t, module.arrived.Get("Authorization"), "the person's session never reaches the module")
}

// The admission the gateway signed is single-use: presented again at the
// module, it is refused as a replay.
func TestHopAnAdmissionCannotBeReplayedAtTheModule(t *testing.T) {
	gw, _, module, moduleServer, bearer := hopHarness(t)
	req := listCollectionRequest(listCollection)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	replay, err := http.NewRequest(http.MethodPost, moduleServer.URL+listCollection, strings.NewReader(`{}`))
	require.NoError(t, err)
	for name, values := range module.arrived {
		if strings.HasPrefix(strings.ToLower(name), "x-codefly-") {
			replay.Header[name] = values
		}
	}
	resp, err := http.DefaultClient.Do(replay)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "replayed")
	require.Equal(t, 1, module.reached)
}

// A call that did not come through the gateway is refused by the module: a
// genuine Work Context alone, and one with an admission signed by a key the
// host never published, admit nothing.
func TestHopACallNotThroughTheGatewayIsRefused(t *testing.T) {
	_, accounts, module, moduleServer, _ := hopHarness(t)
	token := accounts.signer.Issue(t, "documents", &basev0.WorkScopeV1{ResourceKind: "documents", Actions: []string{"read"}})

	call := func(md metadata.MD) (int, string) {
		req, err := http.NewRequest(http.MethodPost, moduleServer.URL+listCollection, strings.NewReader(`{}`))
		require.NoError(t, err)
		for key, values := range md {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	code, _ := call(metadata.Pairs(hop.WorkContextKey, token))
	require.Equal(t, http.StatusUnauthorized, code, "a Work Context without an admission")
	code, _ = call(hoptest.Forward(t, token, "an-unpublished-hop", listCollection))
	require.Equal(t, http.StatusUnauthorized, code, "an admission signed by a key the host does not publish")
	require.Zero(t, module.reached)
}

// A composed workload calls a module through the gateway's hop face with
// a Work Context addressed to it, and the module admits it.
func TestHopAWorkloadCallThroughTheHopFaceIsAdmitted(t *testing.T) {
	gw, accounts, module, _, _ := hopHarness(t)
	token := accounts.signer.Issue(t, "documents", &basev0.WorkScopeV1{ResourceKind: "documents", Actions: []string{"read"}})
	req := listCollectionRequest(listCollection)
	req.Header.Set(hop.WorkContextKey, token)
	w := httptest.NewRecorder()
	gw.hop.face.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, module.reached)
}

// A module's gRPC call reaches the hop endpoint as gRPC over HTTP/2, beside
// Connect and HTTP on the same listener, and the module behind it admits it.
func TestHopAGRPCCallOnTheHopEndpointIsAdmitted(t *testing.T) {
	t.Setenv("CODEFLY__ENVIRONMENT", "local")
	accounts := &hostAuthority{signer: hoptest.New(t), t: t}
	const procedure = "/records.v1.RecordsService/Get"
	var reached atomic.Int32
	var verifier *hop.Verifier
	module := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		md, _ := metadata.FromIncomingContext(stream.Context())
		method, _ := grpc.MethodFromServerStream(stream)
		_, done, err := verifier.Admit(stream.Context(), md, method)
		if err != nil {
			return err
		}
		defer done()
		if err := stream.RecvMsg(new(emptypb.Empty)); err != nil {
			return err
		}
		reached.Add(1)
		return stream.SendMsg(wrapperspb.String("record"))
	}))
	moduleListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = module.Serve(moduleListener) }()
	t.Cleanup(module.Stop)

	routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{{
		Audience: "records", Service: "records", Endpoint: "grpc", Transport: "grpc",
		Address: "http://" + moduleListener.Addr().String(), Package: "records", Version: "0.1.0", Digest: "sha256:r",
		Methods: []string{procedure},
	}}}
	raw, err := json.Marshal(routes)
	require.NoError(t, err)
	_, front, err := newHopRole(t.Context(), accounts, workloadCredential, []string{"records"}, string(raw), routes, "")
	require.NoError(t, err)
	t.Cleanup(front.Close)
	verifier, err = hop.NewVerifier(t.Context(), accounts, "records", hoptest.Replay())
	require.NoError(t, err)

	hopServer := httptest.NewUnstartedServer(hopFace(grpc.NewServer(front.ServerOptions...), front.Handler))
	hopServer.Config.Protocols = new(http.Protocols)
	hopServer.Config.Protocols.SetHTTP1(true)
	hopServer.Config.Protocols.SetUnencryptedHTTP2(true)
	hopServer.Start()
	t.Cleanup(hopServer.Close)
	conn, err := grpc.NewClient(strings.TrimPrefix(hopServer.URL, "http://"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	token := accounts.signer.Issue(t, "records", &basev0.WorkScopeV1{ResourceKind: "records", Actions: []string{"read"}})
	out := new(wrapperspb.StringValue)
	err = conn.Invoke(metadata.AppendToOutgoingContext(t.Context(), hop.WorkContextKey, token), procedure, &emptypb.Empty{}, out)
	require.NoError(t, err)
	require.Equal(t, "record", out.GetValue())
	require.EqualValues(t, 1, reached.Load())

	// Without a Work Context the hop refuses before the module is reached.
	err = conn.Invoke(t.Context(), procedure, &emptypb.Empty{}, out)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	require.EqualValues(t, 1, reached.Load())
}
