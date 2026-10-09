package business_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/apisource"
	"accounts/pkg/datasource/connector/connectortest"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type operationStore struct {
	*datasourceFakeStore
	declarations []operations.Declaration
	attempts     map[string]business.SourceOperationAttempt
	allowed      bool
}

func (s *operationStore) GetOrgMembership(context.Context, string, string) (*gen.OrgMembership, error) {
	return &gen.OrgMembership{Role: gen.OrgRole_ORG_ROLE_ADMIN}, nil
}
func (s *operationStore) CanReadScopeNode(context.Context, string, string, gen.SubjectKind, string, string, string) (bool, error) {
	return s.allowed, nil
}
func (s *operationStore) ListSourceOperations(context.Context, string, string) ([]operations.Declaration, error) {
	return s.declarations, nil
}
func (s *operationStore) ReplaceSourceOperations(_ context.Context, _, _ string, d []operations.Declaration) error {
	s.declarations = d
	return nil
}
func (s *operationStore) GetSourceOperationAttempt(_ context.Context, _, id string) (*business.SourceOperationAttempt, error) {
	a, ok := s.attempts[id]
	if !ok {
		return nil, nil
	}
	return &a, nil
}
func (s *operationStore) CreateSourceOperationAttempt(_ context.Context, a business.SourceOperationAttempt) (bool, error) {
	if _, ok := s.attempts[a.EffectID]; ok {
		return false, nil
	}
	s.attempts[a.EffectID] = a
	return true, nil
}
func (s *operationStore) DeleteSourceOperationAttempt(_ context.Context, _, id string) error {
	delete(s.attempts, id)
	return nil
}

type operationFixture struct {
	service        *business.Service
	store          *operationStore
	secret         string
	calls          int
	responseStatus int
	responseBody   string
	loseReply      bool
	provider       *httptest.Server
}

func newOperationFixture(t *testing.T) *operationFixture {
	t.Helper()
	seed := make([]byte, 48)
	_, err := rand.Read(seed)
	require.NoError(t, err)
	f := &operationFixture{secret: base64.RawURLEncoding.EncodeToString(seed), responseStatus: 200, responseBody: `{}`}
	f.provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.calls++
		if r.Header.Get("Authorization") != "Bearer "+f.secret {
			t.Error("provider did not receive source credential")
		}
		if f.loseReply {
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			_ = conn.Close()
			return
		}
		w.WriteHeader(f.responseStatus)
		_, _ = w.Write([]byte(f.responseBody))
	}))
	t.Cleanup(f.provider.Close)
	f.store = &operationStore{datasourceFakeStore: newDatasourceFakeStore(), attempts: map[string]business.SourceOperationAttempt{}, allowed: true}
	cipher := purposeCipher{}
	ref, err := cipher.EncryptSecret(t.Context(), business.DatasourceConnectorSecretPurpose("source"), f.secret)
	require.NoError(t, err)
	f.store.sources["source"] = &business.DatasourceSource{ID: "source", OrgID: "org", Provider: business.DatasourceProviderAPI, BoundaryNodeID: "boundary", CredentialSecretRef: ref, API: &business.APIDatasourceConfig{BaseURL: f.provider.URL, CredentialKind: business.APICredentialKindBearer}}
	f.service, err = business.NewService(f.store)
	require.NoError(t, err)
	f.service.SetDatasourceConnector(cipher, nil, "")
	// Only the provider transport is substituted; apisource's HTTP and resolved-IP
	// guard have their own transport suite. Business still owns every declaration,
	// authority, cipher, scheduler, durable-attempt and output decision here.
	f.service.SetSourceOperationClientForTest(func(cfg apisource.Config, credential string) business.APIOperationClient {
		return operationHTTPClient{credential: credential}
	})
	return f
}

type operationHTTPClient struct{ credential string }

func (c operationHTTPClient) Do(ctx context.Context, method, target string, body []byte) (*apisource.Result, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(string(body)))
	if err != nil {
		return nil, &apisource.Failure{Reason: "transport failed"}
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	// Match the production single-exchange transport. A shared default
	// transport can transparently resend a GET after a lost reused connection.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, &apisource.Failure{Reason: "transport failed"}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &apisource.Failure{Reason: "provider refused", ProviderStatus: res.StatusCode, RetryAfter: time.Minute}
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &apisource.Failure{Reason: "reply lost"}
	}
	return &apisource.Result{Body: data, StatusCode: res.StatusCode}, nil
}
func (f *operationFixture) Declare(t *testing.T, declarations []operations.Declaration) {
	t.Helper()
	f.store.declarations = nil
	for _, d := range declarations {
		admitted, err := operations.Admit(d)
		require.NoError(t, err)
		f.store.declarations = append(f.store.declarations, admitted)
	}
}
func (f *operationFixture) Invoke(ctx context.Context, name, effect string, input json.RawMessage) error {
	digest := sha256.Sum256(append([]byte(name), input...))
	ctx = receipts.WithEffect(ctx, receipts.Effect{ID: effect, Tenant: "org", Method: "/saas.accounts.v1.DatasourceService/InvokeSourceOperation", RequestDigest: digest[:]})
	_, err := f.service.InvokeSourceOperation(ctx, "actor", "org", "source", name, input, func(context.Context, *business.SourceOperationResult) error { return nil })
	return err
}
func (f *operationFixture) ProviderReply(code int, body string, lose bool) {
	f.responseStatus = code
	f.responseBody = body
	f.loseReply = lose
}
func (f *operationFixture) ProviderCalls() int { return f.calls }
func (f *operationFixture) Credential() string { return f.secret }

func TestAPIOperationsConformance(t *testing.T) {
	connectortest.RunOperations(t, func(t *testing.T) connectortest.OperationsFixture { return newOperationFixture(t) })
}

func TestSourceOperationCurrentAuthorityAndDeclaration(t *testing.T) {
	f := newOperationFixture(t)
	f.Declare(t, []operations.Declaration{connectortest.OperationDeclaration()})
	effect := "11111111-1111-4111-8111-111111111111"
	require.NoError(t, f.Invoke(t.Context(), "read_item", effect, json.RawMessage(`{"id":"a"}`)))
	f.store.allowed = false
	require.Equal(t, codes.PermissionDenied, status.Code(f.Invoke(t.Context(), "read_item", effect, json.RawMessage(`{"id":"a"}`))))
	f.store.allowed = true
	f.store.sources["source"].PersonalOwnerUserID = "other"
	require.Equal(t, codes.PermissionDenied, status.Code(f.Invoke(t.Context(), "read_item", effect, json.RawMessage(`{"id":"a"}`))))
	f.store.sources["source"].PersonalOwnerUserID = ""
	d := connectortest.OperationDeclaration()
	d.Path = "/changed/{id}"
	f.Declare(t, []operations.Declaration{d})
	require.Equal(t, codes.FailedPrecondition, status.Code(f.Invoke(t.Context(), "read_item", effect, json.RawMessage(`{"id":"a"}`))))
	require.Equal(t, 1, f.calls)
}

func TestSourceOperationTransportOutputRefusal(t *testing.T) {
	for _, effect := range []string{operations.ReadOnly, operations.Mutation} {
		t.Run(effect, func(t *testing.T) {
			f := newOperationFixture(t)
			d := connectortest.OperationDeclaration()
			d.Effect = effect
			f.Declare(t, []operations.Declaration{d})
			f.service.SetSourceOperationClientForTest(func(apisource.Config, string) business.APIOperationClient {
				return operationFailureClient{&apisource.Failure{Reason: "response exceeds declared bound", ProviderStatus: 200, OutputRefused: true}}
			})
			err := f.Invoke(t.Context(), d.Name, "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a"}`))
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			if effect == operations.Mutation {
				require.Contains(t, err.Error(), "outcome is unknown")
			}
		})
	}
}

type operationFailureClient struct{ err error }

func (c operationFailureClient) Do(context.Context, string, string, []byte) (*apisource.Result, error) {
	return nil, c.err
}

func TestSourceOperationAuditContainsOnlyOutcomeMetadata(t *testing.T) {
	f := newOperationFixture(t)
	f.Declare(t, []operations.Declaration{connectortest.OperationDeclaration()})
	audit := &recordingAudit{}
	f.service.SetAuditEmitter(audit)
	require.NoError(t, f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"private-input-value"}`)))
	rows := audit.entriesOf(business.EventDatasourceOperationInvoked)
	require.Len(t, rows, 1)
	require.Equal(t, "committed", rows[0].Payload["outcome"])
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-input-value")
	connectortest.AssertNoCredentialSegments(t, []string{f.secret}, string(encoded))
}
