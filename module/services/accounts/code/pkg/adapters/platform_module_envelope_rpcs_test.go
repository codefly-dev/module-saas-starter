package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// moduleAuthorityStore is a super_admin's view of one host: the bindings whose
// manifests were applied, and an envelope store holding at most one revision.
type moduleAuthorityStore struct {
	unenrolledSuperAdminStore
	records []*business.SolutionHostBindingRecord
}

func (*moduleAuthorityStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *moduleAuthorityStore) ListSolutionHostBindings(context.Context) ([]*business.SolutionHostBindingRecord, error) {
	return s.records, nil
}

type oneEnvelopeRevision struct {
	business.ModuleEnvelopeStore
	record *business.ModuleEnvelopeRecord
}

func (s oneEnvelopeRevision) LatestModuleEnvelopeRecord(context.Context) (*business.ModuleEnvelopeRecord, error) {
	return s.record, nil
}

// appliedAsking is the binding record of an applied manifest asking for
// authority under principal.
func appliedAsking(t *testing.T, principal string) *business.SolutionHostBindingRecord {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Binding: "acme.test." + principal, Alias: principal,
		Kind: solutionhost.KindModule, Generation: 2,
		Host:      solutionhost.HostTarget{Coordinate: "acme-test", Domain: "acme-test"},
		Release:   solutionhost.Release{Publisher: "acme", Name: principal, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: principal, Container: "app"}},
		Authority: &solutionhost.Authority{Principal: principal, Queues: []string{principal + ".render"}},
	}
	require.NoError(t, manifest.Validate())
	document, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)
	return &business.SolutionHostBindingRecord{BindingID: manifest.Binding, Applied: &business.SolutionHostBindingApplied{
		SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{Generation: 2, Document: string(document)},
	}}
}

// The envelope read answers, beside the ceiling, what every applied manifest
// asks for and whether the ceiling names its principal — the principals a
// super_admin must add before those modules can mint anything.
func TestTheEnvelopeReadNamesThePrincipalsItDoesNotAllow(t *testing.T) {
	store := &moduleAuthorityStore{records: []*business.SolutionHostBindingRecord{appliedAsking(t, "reports"), appliedAsking(t, "documents")}}
	installLayeredAuthzService(t, store)
	ctx := stampVerifiedIdentity(context.Background(), platformActorID, "", auth.Assurance{})
	read := func() *gen.GetModuleEnvelopeResponse {
		response, err := (&PlatformAdminServer{}).GetModuleEnvelope(ctx, &gen.GetModuleEnvelopeRequest{})
		require.NoError(t, err)
		return response
	}
	allowed := func(response *gen.GetModuleEnvelopeResponse) map[string]bool {
		out := map[string]bool{}
		for _, request := range response.GetRequested() {
			out[request.GetPrincipal()] = request.GetAllowed()
			require.Contains(t, request.GetAuthority(), `"principal":"`+request.GetPrincipal()+`"`)
			require.Equal(t, "acme.test."+request.GetPrincipal(), request.GetBinding())
			require.EqualValues(t, 2, request.GetGeneration())
		}
		return out
	}

	service.SetModuleEnvelopeStore(oneEnvelopeRevision{})
	response := read()
	require.Nil(t, response.Envelope, "nothing has been written")
	require.Equal(t, map[string]bool{"documents": false, "reports": false}, allowed(response))
	require.Equal(t, "documents", response.GetRequested()[0].GetPrincipal(), "ordered by principal")

	service.SetModuleEnvelopeStore(oneEnvelopeRevision{record: &business.ModuleEnvelopeRecord{
		Revision: 1, WrittenBy: platformActorID, WrittenAt: time.Now(),
		Document: []byte(`{"revision":1,"principals":{"reports":{"grant":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","queues":["reports.render"]}}}}`),
	}})
	response = read()
	require.EqualValues(t, 1, response.GetEnvelope().GetRevision())
	require.Equal(t, map[string]bool{"documents": false, "reports": true}, allowed(response))
}
