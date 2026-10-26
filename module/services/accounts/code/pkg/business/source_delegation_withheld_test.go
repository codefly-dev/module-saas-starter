//go:build !race_only

package business_test

// A CONNECT THAT RECORDS NO DELEGATION, THROUGH THE REGISTRY PRODUCTION DERIVES
// (issue #1165).
//
// Every other test of these paths states the registry with
// moduleregistrytest.Parse — the module's own DECLARATION. Production never
// does: it runs the reconcile, which intersects the applied module manifests
// against the envelope a platform super_admin wrote, and a binding the envelope
// does not list is omitted by name and carried on from. On a real cell the
// omitted binding was the one carrying `source_delegation_scopes`, so
// SourceDelegationTargets answered nothing, the connect recorded nothing,
// reported success, and every sync afterwards told the person to connect or
// reconnect the source — which is what they had just done.
//
// So these tests derive the registry the way the pass does, with a real
// envelope and a real signed carrier, and assert the END-TO-END property rather
// than any unit's contract: a reconnect of a source, in a deployment composing
// a module that accepts delegations, either leaves an ACTIVE delegation or
// refuses.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	withheldPrefix = "docstore"
	// withheldDelegationBinding is the binding an envelope entry carries
	// `source_delegation_scopes` on, and withheldOtherBinding one it always
	// allows — two, so that withholding one leaves the PRINCIPAL in the
	// registry with a binding missing, which is the cell's exact shape.
	withheldDelegationBinding = "source-sync"
	withheldOtherBinding      = "run"
	withheldManifestBinding   = "acme.test." + withheldPrefix
	withheldSigner            = "example-release"
	withheldDomain            = "acme"
	withheldCoordinate        = "acme/test/eu-west-1"
)

// withheldManifest is the module's applied, live manifest: one workload, and an
// authority request for both bindings.
func withheldManifest(t *testing.T) *solutionhost.ModuleManifest {
	t.Helper()
	manifest := &solutionhost.ModuleManifest{
		Schema: solutionhost.SchemaModuleManifestV1, Kind: solutionhost.KindModule,
		Binding: withheldManifestBinding, Alias: withheldPrefix, Generation: 1,
		Host:      solutionhost.HostTarget{Coordinate: withheldCoordinate, Domain: withheldDomain},
		Release:   solutionhost.Release{Publisher: "acme", Name: withheldPrefix, Version: "1.0.0"},
		Workloads: []solutionhost.Workload{{Service: "api", Container: "api"}},
		Authority: &solutionhost.Authority{
			Principal: withheldPrefix,
			Bindings: []solutionhost.AuthorityBinding{
				{ID: withheldOtherBinding, Audience: "docstore-run", Operations: map[string][]string{
					"invoke": {"collections:read"},
				}},
				{ID: withheldDelegationBinding, Audience: "docstore-ingest", Operations: map[string][]string{
					"invoke": {"collections:read", "collections:write"},
				}},
			},
			Namespaces:    []string{"collections"},
			ResourceKinds: []string{"collections"},
		},
	}
	require.NoError(t, manifest.Validate(), "the fixture must be a valid manifest")
	return manifest
}

// withheldEnvelope is the ceiling. allowDelegation decides whether the entry
// lists the delegation binding at all: withholding it is what the cell's
// hand-written envelope did.
func withheldEnvelope(t *testing.T, allowDelegation bool) *business.ModuleEnvelope {
	t.Helper()
	audiences := map[string]any{
		withheldOtherBinding: map[string]any{
			"audience":      "docstore-run",
			"invoke_scopes": []any{map[string]any{"resource_kind": "collections", "actions": []any{"read"}}},
		},
	}
	if allowDelegation {
		audiences[withheldDelegationBinding] = map[string]any{
			"audience":                 "docstore-ingest",
			"invoke_scopes":            []any{map[string]any{"resource_kind": "collections", "actions": []any{"read", "write"}}},
			"source_delegation_scopes": []any{map[string]any{"resource_kind": "collections", "actions": []any{"write"}}},
		}
	}
	raw, err := json.Marshal(map[string]any{
		"revision": 1,
		"principals": map[string]any{
			withheldPrefix: map[string]any{"grant": map[string]any{
				"tenant":              testOrg,
				"namespaces":          []any{"collections"},
				"resources":           []any{"collections"},
				"operation_audiences": audiences,
			}},
		},
	})
	require.NoError(t, err)
	envelope, err := business.ParseModuleEnvelope(raw)
	require.NoError(t, err)
	return envelope
}

// withheldEnvelopeSource is the ceiling the pass reads on every pass.
type withheldEnvelopeSource struct{ envelope *business.ModuleEnvelope }

func (s withheldEnvelopeSource) CurrentModuleEnvelope(context.Context) (*business.ModuleEnvelope, error) {
	return s.envelope, nil
}

// withheldInbox is the delivery inbox the pass re-verifies the applied manifest
// against: one carrier, signed by a pinned key, for one generation.
type withheldInbox struct {
	record *business.SolutionDeliveryRecord
}

func (i withheldInbox) ReadDeliveredDocument(_ context.Context, binding string, generation uint64) (*business.SolutionDeliveryRecord, error) {
	if i.record == nil || i.record.Binding != binding || i.record.Generation != generation {
		return nil, nil
	}
	return i.record, nil
}

func (i withheldInbox) RecordDeliveredDocument(context.Context, *business.SolutionDeliveryRecord) (bool, error) {
	return false, nil
}

func (i withheldInbox) ListNewestDeliveredDocuments(context.Context) ([]*business.SolutionDeliveryRecord, error) {
	return []*business.SolutionDeliveryRecord{i.record}, nil
}

// withheldStore is the delegation fake plus the two control-plane reads the
// pass makes: the applied binding records, in a read snapshot.
type withheldStore struct {
	*delegateTriggerStore
	applied []*business.SolutionHostBindingRecord
}

func (withheldStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (withheldStore) WithControlPlaneReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s withheldStore) ListSolutionHostBindings(context.Context) ([]*business.SolutionHostBindingRecord, error) {
	return s.applied, nil
}

// withheldHost is a host whose module's manifest is applied and live, deriving
// its registry inside the given ceiling.
type withheldHost struct {
	svc   *business.Service
	store *withheldStore
	pass  *business.SolutionHostBindingReconciler
}

func newWithheldHost(t *testing.T, envelope *business.ModuleEnvelope) *withheldHost {
	t.Helper()
	manifest := withheldManifest(t)
	payload, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	digest, err := manifest.Digest()
	require.NoError(t, err)
	canonical, err := solutionhost.Marshal(manifest)
	require.NoError(t, err)

	store := &withheldStore{
		delegateTriggerStore: newDelegateTriggerStore(t),
		applied: []*business.SolutionHostBindingRecord{{
			BindingID: manifest.Binding,
			Applied: &business.SolutionHostBindingApplied{
				SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{
					Generation: manifest.Generation, Digest: digest, Document: string(canonical),
				},
			},
		}},
	}
	svc, _ := newDatasourceService(store, &recordingProducer{}, &fakeGitHub{defaultBranch: "main", commit: "abc"})
	verifier := deliverytest.Verifier(t, map[string][]string{withheldSigner: {withheldDomain}})
	svc.SetSolutionDelivery(verifier, withheldInbox{record: &business.SolutionDeliveryRecord{
		Binding: manifest.Binding, Generation: manifest.Generation, ContentHash: digest,
		Payload: payload, Envelope: deliverytest.Envelope(t, payload, withheldSigner),
	}}, nil)
	pass, err := business.NewSolutionHostBindingReconciler(svc, business.SolutionHostBindingReconcilerConfig{
		Source:     &deliveredNothing{},
		Verifier:   verifier,
		Coordinate: withheldCoordinate,
		Interval:   time.Hour,
		Envelopes:  withheldEnvelopeSource{envelope: envelope},
	})
	require.NoError(t, err)
	host := &withheldHost{svc: svc, store: store, pass: pass}
	// The first pass's refusals are the subject of each test, which runs the
	// pass again and asserts on what it returned.
	_ = host.derive(t)
	return host
}

// derive runs one registry pass and keeps its refusals: the pass reports an
// omitted binding and carries on, which is the behaviour under test.
func (h *withheldHost) derive(t *testing.T) error {
	t.Helper()
	err := h.pass.ModulePrincipals().RunOnce(context.Background())
	require.NotErrorIs(t, err, business.ErrModulePrincipalInputsUnreadable, "the pass must be able to read its inputs")
	return err
}

// deliveredNothing is a mount with nothing new in it: these tests are about the
// registry derived from what is ALREADY applied.
type deliveredNothing struct{}

func (deliveredNothing) Documents(context.Context) ([]business.SolutionHostBindingDocument, error) {
	return nil, nil
}

// THE REGRESSION. The envelope withholds the binding that accepts delegations,
// so the registry holds the principal and not that binding — and a connect must
// refuse rather than report a source connected that no module can ever sync.
func TestConnectRefusesWhenTheEnvelopeWithheldTheDelegationBinding(t *testing.T) {
	host := newWithheldHost(t, withheldEnvelope(t, false))
	require.ErrorIs(t, host.derive(t), business.ErrModulePrincipalBindingOmitted,
		"the pass omits the binding by name and carries on, which is what the connect has to notice")

	_, err := host.svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})
	require.Error(t, err, "a connect that records no delegation reported success: issue #1165")
	require.Equal(t, codes.FailedPrecondition, status.Code(err),
		"the person is told to wait for an administrator, not to retry a transient failure")
	require.Contains(t, err.Error(), withheldPrefix, "the refusal must name the module")
	require.Contains(t, err.Error(), withheldDelegationBinding, "and the binding the envelope withheld")
	require.Empty(t, host.store.delegations, "the refusal left a delegation behind")
	// The refusal is returned from inside the connect's own transaction, so in
	// production the source and the credential go back with it. That rollback
	// is Postgres's and this fake's WithOrgTx has none, so it is not asserted
	// here — what is asserted is that the error reaches the caller, which is
	// what the portal's Data sources surface shows the person.

	// And the reason is on the asking manifest's own row, which is where an
	// operator reads a delivery this host could not take whole.
	reasons := host.pass.ModulePrincipals().WithheldAuthorityReasons()
	require.Contains(t, reasons, withheldManifestBinding)
	require.Contains(t, reasons[withheldManifestBinding], withheldDelegationBinding)
	require.Contains(t, reasons[withheldManifestBinding], "the envelope allows no such operation binding")
	require.NotContains(t, reasons[withheldManifestBinding], withheldOtherBinding,
		"a binding the envelope allowed is not reported as withheld")
}

// The same manifest, the same pass, an envelope that allows the binding: the
// connect records an ACTIVE delegation. Without this the test above would pass
// on a host that refuses every connect.
func TestConnectRecordsTheDelegationWhenTheEnvelopeAllowsTheBinding(t *testing.T) {
	host := newWithheldHost(t, withheldEnvelope(t, true))
	require.NoError(t, host.derive(t), "an envelope allowing every binding asked for omits nothing")

	source, err := host.svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})
	require.NoError(t, err)

	active, err := host.svc.ListSourceDelegations(context.Background(), testOrg, source.ID, false)
	require.NoError(t, err)
	require.Len(t, active, 1, "the composed module that accepts delegations must hold one")
	require.Equal(t, withheldPrefix, active[0].ModulePrefix)
	require.Equal(t, withheldDelegationBinding, active[0].BindingID)
	require.True(t, active[0].Active())
	require.Empty(t, host.pass.ModulePrincipals().WithheldAuthorityReasons(),
		"a pass that granted every binding asked for withholds nothing")
}

// The other shape that leaves the registry without the delegation binding: the
// envelope declares no ceiling for the principal at all, so the pass omits the
// whole module and no binding-level omission is reported for it. A connect must
// refuse here too — the first draft of this fix noticed only the binding-level
// omission and would have reported success for this one.
func TestConnectRefusesWhenTheEnvelopeDeclaresNoCeilingForTheModule(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"revision": 1, "principals": map[string]any{}})
	require.NoError(t, err)
	envelope, err := business.ParseModuleEnvelope(raw)
	require.NoError(t, err)
	host := newWithheldHost(t, envelope)
	require.ErrorIs(t, host.derive(t), business.ErrModulePrincipalOmitted)

	_, err = host.svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), withheldDelegationBinding)
	require.Contains(t, err.Error(), "the registry holds no entry for this module principal")
	require.Empty(t, host.store.delegations)
}

// A deployment where NO composed module asks for a delegation binding records
// nothing and is right to: that silent zero is the legitimate shape, and only
// the two together make the shapes distinguishable.
func TestConnectRecordsNothingWhenNoManifestAsksForADelegationBinding(t *testing.T) {
	host := newWithheldHost(t, withheldEnvelope(t, true))
	// The envelope allows the binding and no manifest asks for authority at
	// all, so the registry is empty with nothing withheld.
	host.store.applied = nil
	require.NoError(t, host.derive(t))

	source, err := host.svc.AddGitHubSource(context.Background(), "actor-1", business.AddGitHubSourceInput{
		OrgID: testOrg, Repo: "acme/docs", Branch: "main", CollectionLabel: "guides", AccessToken: "t",
	})
	require.NoError(t, err, "a deployment composing no delegating module must still connect sources")
	require.Empty(t, host.store.delegations)
	require.NotEmpty(t, source.ID)
}
