//go:build !pure

package infra_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/module-saas-starter/module/frontendpayload"
	"github.com/stretchr/testify/require"
)

type frontendBlobSource map[solutionhost.PayloadDigest][]byte

func (s frontendBlobSource) Open(_ context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	data, ok := s[digest]
	if !ok {
		return nil, errors.New("missing bundle")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func frontendFixture(t *testing.T, version string) (*solutionhost.Frontend, []byte) {
	t.Helper()
	dir := t.TempDir()
	for name, value := range map[string]string{
		"mf-manifest.json": `{"metaData":{"globalName":"example","publicPath":"auto","remoteEntry":{"name":"entry.js","type":"global"}},"exposes":[{"path":"./Page","assets":{"js":{"async":["lazy.js"]}}}]}`,
		"entry.js":         "import('./lazy.js')", "lazy.js": version,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0600))
	}
	data, digest, err := composition.CanonicalArchive(dir)
	require.NoError(t, err)
	return &solutionhost.Frontend{Title: "Example", ExposedModule: "./Page", HostContract: 1, Payload: &solutionhost.FrontendPayload{Artifact: "frontend", Digest: solutionhost.PayloadDigest(digest), MediaType: "application/x-tar"}}, data
}

func TestFrontendAppliedHistoryPreservesUpdatesRollbackAndWithdrawal(t *testing.T) {
	delivery := newProductionPathDelivery(t)
	a, rawA := frontendFixture(t, "A")
	b, rawB := frontendFixture(t, "B")
	source := frontendBlobSource{a.Payload.Digest: rawA}
	delivery.service.SetFrontendArtifactSource(source)
	reconciler := newProductionPathReconciler(t, delivery.service)
	alias := uniqueAlias("frontend")
	namespace := "ns-" + alias
	delivery.callerNamespace = namespace
	document := productionPathBinding(t, alias, namespace, 1)
	document.Frontend = a
	apply := func() error {
		requireDelivered(t, delivery.post(t, business.SolutionDeliveryPresence, productionPathCarrier(t, document)))
		return reconciler.RunOnce(testCtx)
	}
	require.NoError(t, apply())

	registrations, _, err := delivery.service.ListSolutionRegistrations(testCtx, false)
	require.NoError(t, err)
	target := ""
	for _, registration := range registrations {
		if registration.Declared.BindingID == document.Binding {
			target = registration.Declared.TargetID
		}
	}
	require.NotEmpty(t, target)
	version := func(generation uint64) string {
		return frontendpayload.AssetVersion(document.Binding, target, generation, document.Release.Identity())
	}
	read := func(service *business.Service, generation uint64, want bool) {
		t.Helper()
		found, err := service.GetRetainedSolutionFrontend(testCtx, document.Binding, version(generation))
		require.NoError(t, err)
		if want {
			require.NotNil(t, found)
			require.Equal(t, target, found.TargetID)
			require.Equal(t, generation, found.Generation)
		} else {
			require.Nil(t, found)
		}
	}
	read(delivery.service, 1, true)
	document.Generation = 2
	document.Frontend = b
	require.ErrorIs(t, apply(), business.ErrSolutionFrontendNotPrepared)
	pending := productionPathBindingRecord(t, delivery.service, document.Binding)
	require.Equal(t, uint64(1), pending.Applied.Generation)
	read(delivery.service, 1, true)
	read(delivery.service, 2, false)
	source[b.Payload.Digest] = rawB
	require.NoError(t, reconciler.RunOnce(testCtx))
	read(delivery.service, 2, true)
	document.Generation = 3
	document.Frontend = a
	require.NoError(t, apply())
	read(delivery.service, 3, true)
	recovered, err := business.NewService(testStore)
	require.NoError(t, err)
	for generation := uint64(1); generation <= 3; generation++ {
		read(recovered, generation, true)
	}
	document.Generation = 4
	unsigned := *a
	unsigned.Payload = nil
	document.Frontend = &unsigned
	require.ErrorIs(t, apply(), business.ErrSolutionFrontendNotPrepared)
	require.Equal(t, uint64(3), productionPathBindingRecord(t, delivery.service, document.Binding).Applied.Generation)
	read(delivery.service, 4, false)
	document.Generation = 5
	document.Frontend = nil
	require.NoError(t, apply())
	read(delivery.service, 1, true)
	read(delivery.service, 5, false)
	document.Generation = 6
	document.Removed = true
	document.Workloads = nil
	document.Routes = nil
	document.Modules = nil
	document.Endpoints = nil
	document.Artifacts = nil
	require.NoError(t, apply())
	for generation := uint64(1); generation <= 3; generation++ {
		read(recovered, generation, false)
	}
}

func TestFrontendRetentionIsNotDiagnosticHistoryLimit(t *testing.T) {
	alias := uniqueAlias("retained")
	binding := productionPathBinding(t, alias, "ns-"+alias, 1)
	target := declarePresence(t, binding.Binding, alias)
	now := time.Now().UTC()
	revision := int64(1)
	// This exercises storage history, not admission. Roll back the synthetic
	// history so the shared inbox does not offer it to another test's reconciler.
	rollback := errors.New("retention fixture complete")
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		for generation := uint64(1); generation <= 61; generation++ {
			binding.Generation = generation
			data, err := solutionhost.SignedPayload(binding)
			require.NoError(t, err)
			digest, err := binding.Digest()
			require.NoError(t, err)
			_, err = testStore.RecordDeliveredDocument(ctx, &business.SolutionDeliveryRecord{Kind: business.SolutionDeliveryPresence, DocumentID: binding.Binding, Generation: generation, ContentHash: digest, Payload: data, Bundle: deliverytest.Envelope(t, data, solutionhost.FixtureDeliveredBy), SignerIdentity: productionPathSigner, OwnershipDomain: productionPathDomain, ReceivedAt: now})
			require.NoError(t, err)
			decision := "applied"
			reason := ""
			if generation == 61 {
				decision = "refused"
				reason = "candidate refused"
			}
			require.NoError(t, testStore.RecordSolutionGenerationDecision(ctx, &business.SolutionGenerationDecision{BindingID: binding.Binding, TargetID: target, Generation: generation, Digest: digest, Release: binding.Release.Identity(), Decision: decision, Reason: reason, RegistryRevision: &revision, DecidedAt: now}))
		}
		for generation := uint64(1); generation <= 61; generation++ {
			version := frontendpayload.AssetVersion(binding.Binding, target, generation, binding.Release.Identity())
			row, err := testStore.GetRetainedSolutionFrontend(ctx, binding.Binding, version, now.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, generation <= 60, row != nil, "window retains applied generation %d beyond diagnostic cap", generation)
			row, err = testStore.GetRetainedSolutionFrontend(ctx, binding.Binding, version, now.Add(time.Hour))
			require.NoError(t, err)
			require.Equal(t, generation >= 51 && generation <= 60, row != nil, "last-ten decision for generation %d", generation)
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
}
