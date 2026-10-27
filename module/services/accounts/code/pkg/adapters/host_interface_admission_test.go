package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-client/go/stamp"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memoryAdmissionKeys stands in for the table: the lease semantics against a
// real database are proven in pkg/infra; here the adapter and the attestation.
type memoryAdmissionKeys struct {
	mu      sync.Mutex
	keys    map[string]business.AdmissionKey
	readErr error
}

func (m *memoryAdmissionKeys) RegisterAdmissionKey(_ context.Context, key business.AdmissionKey, lease time.Duration, max int) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, renewed := m.keys[key.ID]
	if !renewed && len(m.keys) >= max {
		return time.Time{}, false, business.ErrAdmissionKeysFull
	}
	key.ExpiresAt = time.Now().Add(lease)
	m.keys[key.ID] = key
	return key.ExpiresAt, !renewed, nil
}

func (m *memoryAdmissionKeys) AdmissionKeys(context.Context) ([]business.AdmissionKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return nil, m.readErr
	}
	out := make([]business.AdmissionKey, 0, len(m.keys))
	for _, key := range m.keys {
		out = append(out, key)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

type admissionFixture struct {
	server  *HostAuthorityServer
	store   *memoryAdmissionKeys
	cluster *executiontest.Cluster
	audit   *recordingAuditEmitter
	hop     string // the hop's workload credential
}

func newAdmissionFixture(t *testing.T) admissionFixture {
	t.Helper()
	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
	cluster := executiontest.New()
	svc.SetExecutionBinding(cluster)
	store := &memoryAdmissionKeys{keys: map[string]business.AdmissionKey{}}
	svc.SetAdmissionKeyStore(store)
	audit := &recordingAuditEmitter{}
	svc.SetAuditEmitter(audit)
	issuer := newCoreTestServer(t, &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{OrganizationRevision: 1}}, newSealAnswersFake(1))
	hop := cluster.Run(executiontest.Workload{Namespace: business.HopModule, ServiceAccount: business.HopService, Container: "app"})
	return admissionFixture{server: NewHostAuthorityServer(issuer), store: store, cluster: cluster, audit: audit, hop: hop}
}

// hopPublicKey is the public key a hop signs with from the seed a label
// hashes to, as stamp.NewSigner derives it, and its admission key id.
func hopPublicKey(t *testing.T, label string) (string, []byte) {
	t.Helper()
	seed := sha256.Sum256([]byte(label))
	public := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	return stamp.KeyID(public), []byte(public)
}

// The id this host publishes is the id the hop signs under: stamp.KeyID,
// imported, never restated. The vector pins it against a change underneath.
func TestAdmissionKeyIDIsTheStampDerivation(t *testing.T) {
	id, public := hopPublicKey(t, "the-hop")
	require.Equal(t, "8a97a61f0c78ad0e2aae1d2da082d42f78d0e40004a5b70ee514021aff24de10", hex.EncodeToString(public))
	require.Equal(t, "hop-ebfff8368cab37ae", id)
}

func (f admissionFixture) register(t *testing.T, credential string, public []byte) (*authorityv1.RegisterAdmissionKeyResponse, error) {
	t.Helper()
	return f.server.RegisterAdmissionKey(t.Context(), &authorityv1.RegisterAdmissionKeyRequest{
		Actor: &authorityv1.WorkloadCredential{Credential: credential}, PublicKey: public,
	})
}

func (f admissionFixture) published(t *testing.T) []string {
	t.Helper()
	keys, err := f.server.Keys(t.Context(), &authorityv1.KeysRequest{})
	require.NoError(t, err)
	var ids []string
	for _, key := range keys.GetAdmissionKeys() {
		ids = append(ids, key.GetKeyId())
	}
	return ids
}

// The hop registers its admission key under its own workload credential, and
// Keys publishes exactly that public key as an admission key, never among the
// Work Context keys, which say what they verify.
func TestTheHopsRegisteredKeyIsPublishedApartFromTheWorkContextKeys(t *testing.T) {
	f := newAdmissionFixture(t)
	require.Empty(t, f.published(t), "a host no hop registered with publishes admission keys")

	id, public := hopPublicKey(t, "the-hop")
	out, err := f.register(t, f.hop, public)
	require.NoError(t, err)
	require.Equal(t, id, out.GetKey().GetKeyId())
	require.Equal(t, admissionKeyPurpose, out.GetKey().GetPurpose())
	require.Equal(t, "Ed25519", out.GetKey().GetAlgorithm())
	require.Equal(t, public, out.GetKey().GetPublicKey())
	lease := time.Until(time.Unix(out.GetExpiresAtUnix(), 0))
	require.True(t, lease > 0 && lease <= business.AdmissionKeyLease, "the registration lapses in %s", lease)

	keys, err := f.server.Keys(t.Context(), &authorityv1.KeysRequest{})
	require.NoError(t, err)
	require.Len(t, keys.GetAdmissionKeys(), 1)
	require.Equal(t, id, keys.GetAdmissionKeys()[0].GetKeyId())
	require.Equal(t, admissionKeyPurpose, keys.GetAdmissionKeys()[0].GetPurpose())
	require.Len(t, keys.GetKeys(), 1)
	for _, key := range keys.GetKeys() {
		require.NotEqual(t, id, key.GetKeyId(), "the hop's admission key is published as a Work Context key")
		require.Equal(t, "work-context", key.GetPurpose())
	}

	// Its first publication is audited, naming the key and the workload.
	published := f.audit.only(t)
	require.Equal(t, business.EventHopAdmissionKeyPublished, published.EventType)
	require.Equal(t, id, published.ResourceID)
	require.Equal(t, id, published.Payload["key_id"])
	require.Equal(t, "authority/hop", published.Payload["workload"])

	// A restart registers the same key again: one entry, renewed, and a
	// renewal is not an event.
	_, err = f.register(t, f.hop, public)
	require.NoError(t, err)
	require.Equal(t, []string{id}, f.published(t))
	require.Len(t, f.audit.entries, 1, "a renewal was audited")
}

// Only the hop registers an admission key: another composed workload, the
// hop's service name in another module's namespace, and a credential the
// cluster never issued are each refused by name, and publish nothing.
func TestOnlyTheHopRegistersAnAdmissionKey(t *testing.T) {
	f := newAdmissionFixture(t)
	_, public := hopPublicKey(t, "impostor")
	for name, tc := range map[string]struct {
		workload executiontest.Workload
		reason   string
	}{
		"another module":        {executiontest.Workload{Namespace: "documents", ServiceAccount: "documents", Container: "app"}, "the caller is documents/documents, not the hop (authority/hop)"},
		"a hop of another name": {executiontest.Workload{Namespace: "documents", ServiceAccount: "hop", Container: "app"}, "the caller is documents/hop, not the hop (authority/hop)"},
		"the authority's other": {executiontest.Workload{Namespace: "authority", ServiceAccount: "edge", Container: "app"}, "the caller is authority/edge, not the hop (authority/hop)"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.register(t, f.cluster.Run(tc.workload), public)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
			require.ErrorContains(t, err, tc.reason)
		})
	}
	_, err := f.register(t, "not-a-credential", public)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.ErrorContains(t, err, "actor credential refused")
	_, err = f.register(t, "", public)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	require.Empty(t, f.published(t), "a refused registration published a key")
	require.Empty(t, f.audit.entries, "a refused registration was audited")

	_, err = f.register(t, f.hop, public[:16])
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	require.ErrorContains(t, err, "16 bytes, not 32")
	require.Empty(t, f.published(t))
}

// The bound is the store's answer, refused by name; an unreadable store fails
// Keys rather than publishing no admission key, which every verifier would
// read as "refuse every admission".
func TestAdmissionKeyRefusalsAndReadFailuresAreNamed(t *testing.T) {
	f := newAdmissionFixture(t)
	for n := range business.MaxAdmissionKeys {
		_, public := hopPublicKey(t, "key-"+string(rune('a'+n)))
		_, err := f.register(t, f.hop, public)
		require.NoError(t, err)
	}
	_, extra := hopPublicKey(t, "one-too-many")
	_, err := f.register(t, f.hop, extra)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.ErrorContains(t, err, business.ErrAdmissionKeysFull.Error())

	f.store.readErr = errors.New("database unreachable")
	_, err = f.server.Keys(t.Context(), &authorityv1.KeysRequest{})
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.ErrorContains(t, err, "database unreachable")
}

// A host with no admission key store says so on both methods.
func TestAnUnwiredAdmissionKeyStoreIsNamed(t *testing.T) {
	f := newAdmissionFixture(t)
	service.SetAdmissionKeyStore(nil)
	_, public := hopPublicKey(t, "the-hop")
	_, err := f.register(t, f.hop, public)
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.ErrorContains(t, err, business.ErrAdmissionKeysUnwired.Error())
	_, err = f.server.Keys(t.Context(), &authorityv1.KeysRequest{})
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
}
