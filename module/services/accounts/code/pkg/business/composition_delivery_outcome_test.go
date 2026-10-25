package business

import (
	"context"
	"errors"
	"testing"

	"accounts/internal/deliverytest"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// The receipt and applied state are separate, as they are in Postgres. The
// reconcile callback below controls only what has committed after the pass.
type compositionOutcomeStore struct {
	reviewControlPlaneStore
	SolutionDeliveryStore
	receipt *SolutionDeliveryRecord
	rows    []*SolutionHostBindingRecord
	readErr error
}

type compositionOutcomeCarrier struct{}

func (compositionOutcomeCarrier) AuthorizeCarrier(_ context.Context, credential, namespace string) error {
	if credential != "Bearer host-delivery" || namespace != "example" {
		return errors.New("not the example delivery account")
	}
	return nil
}

func (s *compositionOutcomeStore) RecordDeliveredDocument(_ context.Context, record *SolutionDeliveryRecord) (bool, error) {
	s.receipt = record
	return true, nil
}

func (s *compositionOutcomeStore) ListNewestDeliveredDocuments(context.Context) ([]*SolutionDeliveryRecord, error) {
	if s.receipt == nil {
		return nil, nil
	}
	return []*SolutionDeliveryRecord{s.receipt}, nil
}

func (s *compositionOutcomeStore) ListSolutionHostBindings(context.Context) ([]*SolutionHostBindingRecord, error) {
	return s.rows, s.readErr
}

func TestApplyCompositionAnswersOnlyItsOwnManifestOutcome(t *testing.T) {
	const unrelated = "another-solution: its private installation refusal"
	for _, tc := range []struct {
		name       string
		outcome    string
		passError  string
		wantError  error
		wantReason string
	}{
		{name: "other binding failed", outcome: "applied", passError: unrelated},
		{name: "fixture installation failed", outcome: "applied", passError: "fixture installation: " + unrelated},
		{name: "own refusal survives aggregate error", outcome: "pending", passError: unrelated, wantError: ErrSolutionDeliveryConflict, wantReason: "own frontend is not prepared"},
		{name: "own refusal without aggregate error", outcome: "pending", wantError: ErrSolutionDeliveryConflict, wantReason: "own frontend is not prepared"},
		{name: "receipt alone is not application", outcome: "missing", passError: unrelated, wantError: ErrSolutionDeliveryHostState, wantReason: "has no applied record"},
		{name: "older applied generation", outcome: "older", passError: unrelated, wantError: ErrSolutionDeliveryConflict, wantReason: "no matching applied generation and digest"},
		{name: "different applied digest", outcome: "digest", passError: unrelated, wantError: ErrSolutionDeliveryConflict, wantReason: "no matching applied generation and digest"},
		{name: "unreadable applied state", outcome: "unreadable", passError: unrelated, wantError: ErrSolutionDeliveryHostState, wantReason: "applied state could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := fixtureManifest(t, "example", 2)
			raw, err := solutionhost.SignedPayload(manifest)
			require.NoError(t, err)
			store := &compositionOutcomeStore{}
			svc := &Service{store: store}
			svc.SetSolutionDelivery(fixtureSigner(t), store, compositionOutcomeCarrier{})
			svc.SetSolutionDeliveryCoordinate(testDeliveryCoordinate)
			svc.SetCompositionReconcile(func(context.Context) error {
				require.NotNil(t, store.receipt, "reconciliation follows authenticated durable receipt")
				own := appliedRecordOf(t, manifest)
				switch tc.outcome {
				case "pending":
					own.Applied = nil
					own.PendingReason = tc.wantReason
				case "older":
					own.Applied.Generation--
				case "digest":
					own.Applied.Digest = "sha256:different"
				case "unreadable":
					store.readErr = errors.New(unrelated)
				}
				// Even a malformed unrelated applied row must not be parsed to
				// answer the caller's binding.
				store.rows = []*SolutionHostBindingRecord{{BindingID: "another-solution", Applied: &SolutionHostBindingApplied{}, PendingReason: unrelated}}
				if tc.outcome != "missing" {
					store.rows = append(store.rows, own)
				}
				if tc.passError != "" {
					return errors.New(tc.passError)
				}
				return nil
			})
			response, err := svc.ApplyComposition(context.Background(), &compositionv1.ApplyManifestRequest{
				Carrier: &compositionv1.SignedManifestV1{Document: raw, Envelope: deliverytest.Envelope(t, raw, testDeliveryKey)}, Credential: "host-delivery",
			})
			if tc.wantError == nil {
				require.NoError(t, err)
				require.Equal(t, manifest.Alias, response.GetPrincipal())
				require.Equal(t, manifest.Generation, response.GetGeneration())
				return
			}
			require.Nil(t, response)
			require.ErrorIs(t, err, tc.wantError)
			require.ErrorContains(t, err, manifest.Binding)
			require.ErrorContains(t, err, tc.wantReason)
			require.NotContains(t, err.Error(), unrelated)
		})
	}
}
