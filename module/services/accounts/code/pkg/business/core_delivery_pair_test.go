package business

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	delivery "github.com/codefly-dev/authority/libraries/delivery/go"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"accounts/internal/deliverytest"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// Blue, 2026-11-11: accounts recorded a directory page's generation 8 as
// applied and current while the authority core refused it, because its roles
// named a task kind and the document kinds through an import binding, which
// the composition did not reach. The core stayed at 7; the two records
// parted, and generation 9 then measured as a widening of 7 and ended three
// of the page's grants. These tests hold accounts to the core's verdict: a
// generation the core refuses is recorded as refused, never as applied, on
// the delivery endpoint (the core's admission, before anything is recorded)
// and on the pass (the core composes first, accounts applies after).

// pairStore keeps one binding row and its decision trail; stopApply ends
// accounts' apply at its first read, so a test tells the apply was reached.
type pairStore struct {
	reviewControlPlaneStore
	row       *SolutionHostBindingRecord
	decisions []*SolutionGenerationDecision
	stopApply bool
}

var errApplyStopped = errors.New("accounts' apply was reached and stopped here")

func (s *pairStore) GetSolutionHostBindingForUpdate(context.Context, string) (*SolutionHostBindingRecord, error) {
	if s.stopApply {
		return nil, errApplyStopped
	}
	return s.row, nil
}

func (s *pairStore) SaveSolutionHostBinding(_ context.Context, record *SolutionHostBindingRecord) error {
	s.row = record
	return nil
}

func (s *pairStore) RecordSolutionGenerationDecision(_ context.Context, decision *SolutionGenerationDecision) error {
	s.decisions = append(s.decisions, decision)
	return nil
}

func (s *pairStore) ListSolutionHostBindings(context.Context) ([]*SolutionHostBindingRecord, error) {
	if s.row == nil {
		return nil, nil
	}
	return []*SolutionHostBindingRecord{s.row}, nil
}

// pairCore is the authority core over a memory store, as this host builds
// it (the host installs modules itself), composing legacy-store, a module
// serving legacy:read alone.
func pairCore(t *testing.T) *authority.Service {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(memstore.New(), authority.Config{Issuer: "test.example", KeyID: "key", Signer: private,
		Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true})
	require.NoError(t, err)
	_, err = core.ApplyModule(context.Background(), domain.Module{Name: "legacy-store", Principal: "legacy-store", Kind: domain.KindModule, Generation: 1,
		ResourceKinds: []*basev0.WorkScopeV1{{ResourceKind: "legacy", Actions: []string{"read"}}}})
	require.NoError(t, err)
	return core
}

// pagesDelivery is the module page pages at generation, binding legacy-store
// for legacy:read, its installer and viewer roles declared in a signed
// authority document; beyondReach names legacy:write through that binding
// for the viewer, which the composition does not reach.
type pagesDelivery struct {
	manifest  *solutionhost.ModuleManifest
	carrier   *solutionhost.Signed
	authority *compositionv1.SignedManifestV1
	delivered *solutionhost.Delivered
	signer    *solutionhost.InMemorySigner
}

func pagesAt(t *testing.T, generation uint64, beyondReach bool) pagesDelivery {
	t.Helper()
	manifest := fixtureManifest(t, "pages", generation)
	manifest.Authority = &solutionhost.Authority{Principal: "pages", ResourceKinds: []string{"pages.entry"}, InstallationRole: domain.NoInstallationRole,
		Bindings: []solutionhost.AuthorityBinding{{ID: "legacy", Audience: "legacy-store", Operations: map[string][]string{"invoke": {"legacy:read"}}}}}
	manifest.Frontend = &solutionhost.Frontend{Title: "Pages", ExposedModule: "./Page", HostContract: 1}
	require.NoError(t, manifest.Validate())
	raw, err := solutionhost.SignedPayload(manifest)
	require.NoError(t, err)
	carrier := &solutionhost.Signed{Document: raw, Envelope: deliverytest.Envelope(t, raw, testDeliveryKey)}
	delivered, err := solutionhost.VerifyDelivered(context.Background(), carrier, fixtureSigner(t))
	require.NoError(t, err)
	entry := func(actions ...string) []*authorityv1.WorkScopeV1 {
		return []*authorityv1.WorkScopeV1{{ResourceKind: "pages.entry", Actions: actions}}
	}
	viewer := &compositionv1.RoleV1{Name: "viewer", Scopes: entry("read")}
	if beyondReach {
		viewer.Through = []*compositionv1.RoleThroughV1{{Binding: "legacy", Scopes: []*authorityv1.WorkScopeV1{{ResourceKind: "legacy", Actions: []string{"write"}}}}}
	}
	document, err := proto.Marshal(&compositionv1.AuthorityManifestV1{Module: "pages", Generation: generation, CoreManifestDigest: delivery.Digest(raw),
		InstallationRole: domain.NoInstallationRole, ScopeCeilings: entry("read", "write"),
		Roles: []*compositionv1.RoleV1{{Name: InstallerRole, Scopes: entry("read", "write")}, viewer}})
	require.NoError(t, err)
	signer := deliverytest.Signer(t, testDeliveryKey)
	envelope, err := delivery.Sign(document, signer.KeyID(), signer.Sign)
	require.NoError(t, err)
	return pagesDelivery{manifest: manifest, carrier: carrier, delivered: delivered, signer: signer,
		authority: &compositionv1.SignedManifestV1{Document: document, Envelope: envelope}}
}

func (d pagesDelivery) inboxRecord() *SolutionDeliveryRecord {
	return &SolutionDeliveryRecord{Binding: d.manifest.Binding, Generation: d.manifest.Generation, Payload: d.carrier.Document, Envelope: d.carrier.Envelope,
		AuthorityDocument: d.authority.Document, AuthorityEnvelope: d.authority.Envelope}
}

func (d pagesDelivery) decision() SolutionHostBindingDecision {
	return SolutionHostBindingDecision{Delivered: d.delivered, Document: d.manifest, Decision: solutionhost.DecisionAccept}
}

// On the pass, the core composes a generation before accounts applies it. A
// generation the core refuses on reach is recorded as refused, with the
// core's reason, and applied nowhere; delivered again it is still refused;
// delivered again at a later generation with the same declaration it is
// still refused. A later generation the core admits is composed, and only
// then does accounts' apply run.
func TestThePassAppliesNothingTheCoreRefuses(t *testing.T) {
	ctx := context.Background()
	core := pairCore(t)
	store := &pairStore{}
	inbox := signedInbox{rows: map[string]*SolutionDeliveryRecord{}}
	service := &Service{store: store, deliveryStore: inbox, authorityCore: &authorityCoreState{core: core}}
	reconciler := &SolutionHostBindingReconciler{service: service, coordinate: testDeliveryCoordinate, now: func() time.Time { return time.Now().UTC() }}
	deliver := func(d pagesDelivery) error {
		inbox.rows[d.manifest.Binding+"@"+strconv.FormatUint(d.manifest.Generation, 10)] = d.inboxRecord()
		return errors.Join(reconciler.applyAdmitted(ctx, d.decision(), "")...)
	}
	notComposed := func() {
		t.Helper()
		_, err := core.Composed(ctx, "pages")
		code, _ := domain.CodeOf(err)
		require.Equal(t, domain.NotFound, code, "the core composes nothing it refused")
		require.Nil(t, store.row.Applied, "accounts applied nothing the core refused")
	}
	refused := func(generation uint64) {
		t.Helper()
		err := deliver(pagesAt(t, generation, true))
		require.ErrorContains(t, err, `role "viewer" of pages names legacy:write through binding "legacy", which reaches it nowhere in the composition`)
		notComposed()
		require.Contains(t, store.row.PendingReason, "the authority core refused generation "+strconv.FormatUint(generation, 10)+": ", "the refusal is the binding's pending reason")
		require.Contains(t, store.row.PendingReason, "reaches it nowhere")
		last := store.decisions[len(store.decisions)-1]
		require.Equal(t, SolutionGenerationRefused, last.Decision, "the generation is refused in its trail")
		require.Equal(t, generation, last.Generation)
		require.Contains(t, last.Reason, "reaches it nowhere")
	}
	refused(8)
	refused(8)
	refused(9)

	// Generation 10 names nothing beyond reach: the core composes it, and
	// accounts' apply runs after, reaching the store.
	store.stopApply = true
	err := deliver(pagesAt(t, 10, false))
	require.ErrorIs(t, err, errApplyStopped, "accounts' apply ran once the core had composed")
	_, refusal := domain.CodeOf(err)
	require.False(t, refusal)
	composed, err := core.Composed(ctx, "pages")
	require.NoError(t, err)
	require.Equal(t, uint64(10), composed.Generation, "the core composed generation 10 before accounts applied it")
	require.Equal(t, SolutionGenerationRefused, store.decisions[len(store.decisions)-1].Decision, "nothing the stopped apply did not record is in the trail")
}

// On the delivery endpoint, a delivery the core would refuse is refused with
// the core's reason before anything is recorded: the core's own admission
// (Admit), read-only, is the precheck.
func TestTheEndpointRefusesWithTheCoresAdmissionBeforeRecording(t *testing.T) {
	ctx := context.Background()
	core := pairCore(t)
	store := &compositionOutcomeStore{}
	svc := &Service{store: store, authorityCore: &authorityCoreState{core: core}}
	svc.SetSolutionDelivery(fixtureSigner(t), store, compositionOutcomeCarrier{})
	svc.SetSolutionDeliveryCoordinate(testDeliveryCoordinate)
	svc.SetCompositionReconcile(func(context.Context) error {
		t.Fatal("a refused delivery reconciles nothing")
		return nil
	})
	beyond := pagesAt(t, 8, true)
	svc.SetAuthorityDocumentVerifier(func(doc, envelope []byte) (string, error) {
		return delivery.Verify(doc, envelope, map[string]crypto.PublicKey{beyond.signer.KeyID(): beyond.signer.Public()})
	})
	_, err := svc.ApplyComposition(ctx, &compositionv1.ApplyManifestRequest{
		Carrier: &compositionv1.SignedManifestV1{Document: beyond.carrier.Document, Envelope: beyond.carrier.Envelope}, Authority: beyond.authority, Credential: "host-delivery"})
	require.ErrorContains(t, err, `role "viewer" of pages names legacy:write through binding "legacy", which reaches it nowhere in the composition`)
	require.Nil(t, store.receipt, "nothing was recorded")
	_, err = core.Composed(ctx, "pages")
	code, _ := domain.CodeOf(err)
	require.Equal(t, domain.NotFound, code)

	// The same question, asked of the core directly, is the same answer.
	within := pagesAt(t, 8, false)
	require.NoError(t, svc.precheckCoreModule(ctx, &compositionv1.ApplyManifestRequest{
		Carrier: &compositionv1.SignedManifestV1{Document: within.carrier.Document}, Authority: within.authority}))
}

// Before the core is built, the pass applies nothing: a generation delivered
// then is recorded as pending with that reason, applied nowhere, and applied
// on the next pass once the core exists. The reconciler's first pass at boot
// runs before the core is configured, and accounts' own admission admits a
// generation the core refused on every pass, so this is what keeps a restart
// from applying one. The endpoint answers the same before anything is
// recorded. A generation the inbox holds no record of is not composed either:
// the core composes a module with its authority document, never without.
func TestThePassAppliesNothingBeforeTheCoreIsBuilt(t *testing.T) {
	ctx := context.Background()
	store := &pairStore{}
	inbox := signedInbox{rows: map[string]*SolutionDeliveryRecord{}}
	service := &Service{store: store, deliveryStore: inbox}
	reconciler := &SolutionHostBindingReconciler{service: service, coordinate: testDeliveryCoordinate, now: func() time.Time { return time.Now().UTC() }}
	deliver := func(d pagesDelivery, held bool) error {
		if held {
			inbox.rows[d.manifest.Binding+"@"+strconv.FormatUint(d.manifest.Generation, 10)] = d.inboxRecord()
		}
		// The pass records what delivery shows before it applies anything (RunOnce).
		require.NoError(t, service.recordSolutionHostBindingDesired(ctx, d.manifest, "", "", false, reconciler.now()))
		return errors.Join(reconciler.applyAdmitted(ctx, d.decision(), "")...)
	}
	within := pagesAt(t, 10, false)
	err := deliver(within, true)
	require.ErrorIs(t, err, ErrAuthorityCoreUnconfigured)
	_, refusal := domain.CodeOf(err)
	require.False(t, refusal, "no verdict: the generation is pending, not refused")
	require.Nil(t, store.row.Applied, "accounts applied nothing before the core was built")
	require.Equal(t, "generation 10 is not composed in the authority core: the authority core is not configured yet", store.row.PendingReason)
	require.Equal(t, uint64(10), store.row.Desired.Generation, "the generation waits as desired")
	require.ErrorIs(t, service.precheckCoreModule(ctx, &compositionv1.ApplyManifestRequest{
		Carrier: &compositionv1.SignedManifestV1{Document: within.carrier.Document}, Authority: within.authority}), ErrAuthorityCoreUnconfigured,
		"the endpoint admits nothing before the core is built")

	// The next pass, with the core built: composed first, then accounts' apply runs.
	core := pairCore(t)
	service.authorityCore = &authorityCoreState{core: core}
	store.stopApply = true
	err = errors.Join(reconciler.applyAdmitted(ctx, within.decision(), "")...)
	require.ErrorIs(t, err, errApplyStopped, "accounts' apply ran once the core had composed")
	composed, err := core.Composed(ctx, "pages")
	require.NoError(t, err)
	require.Equal(t, uint64(10), composed.Generation)

	// A generation the inbox holds no record of composes nothing.
	store.stopApply = false
	err = deliver(pagesAt(t, 11, false), false)
	require.ErrorContains(t, err, "which the inbox does not hold")
	_, refusal = domain.CodeOf(err)
	require.False(t, refusal)
	composed, err = core.Composed(ctx, "pages")
	require.NoError(t, err)
	require.Equal(t, uint64(10), composed.Generation, "the core still holds 10")
	require.Contains(t, store.row.PendingReason, "generation 11 is not composed in the authority core: ")
}
