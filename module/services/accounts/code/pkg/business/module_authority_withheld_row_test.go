package business

// WHERE AN OPERATOR READS A WITHHELD BINDING (issue #1165).
//
// `module operation audience omitted from the registry` was a WARN among boot
// noise and nothing else. On a real cell one pass was dropping five bindings
// and no surface said so, so the module that could not be delegated a source
// looked healthy while every sync refused.
//
// The reason now reaches the asking binding's own row — the row that already
// carries why this host could not take a delivery — through the one write the
// pass already makes per binding. These tests hold it to the two things that
// distinguishes it from an admission refusal: it IS the row's pending reason,
// and it is NOT a refusal of the generation, which was applied.

import (
	"context"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

// desiredRowStore keeps one binding row and every generation decision written
// against it.
type desiredRowStore struct {
	reviewControlPlaneStore
	row       *SolutionHostBindingRecord
	decisions []*SolutionGenerationDecision
}

func (s *desiredRowStore) GetSolutionHostBindingForUpdate(context.Context, string) (*SolutionHostBindingRecord, error) {
	return s.row, nil
}

func (s *desiredRowStore) SaveSolutionHostBinding(_ context.Context, record *SolutionHostBindingRecord) error {
	s.row = record
	return nil
}

func (s *desiredRowStore) RecordSolutionGenerationDecision(_ context.Context, decision *SolutionGenerationDecision) error {
	s.decisions = append(s.decisions, decision)
	return nil
}

// withheldRowDocument is a live module manifest asking for one binding.
func withheldRowDocument(t *testing.T) *solutionhost.ModuleManifest {
	t.Helper()
	return testManifest(t, map[string][]string{"invoke": {"profiles:read"}})
}

func TestAWithheldBindingIsThePendingReasonAndNotARefusedGeneration(t *testing.T) {
	store := &desiredRowStore{}
	service := &Service{store: store}
	document := withheldRowDocument(t)
	now := time.Now().UTC()
	reason := `module operation audience omitted from the registry: "reports" binding "run": the envelope allows no such operation binding`

	// The generation is applied (settled) and the envelope withheld a binding
	// its manifest asked for.
	require.NoError(t, service.recordSolutionHostBindingDesired(context.Background(), document, "", reason, true, now))
	require.Equal(t, reason, store.row.PendingReason, "the withheld binding is not on the row an operator reads")
	require.NotNil(t, store.row.PendingSince)
	require.Empty(t, store.decisions,
		"the generation was APPLIED; recording it as refused would borrow a status word that claims something else")

	// A standing defect keeps the instant it started: a pass every interval
	// must not make a day-old defect read as seconds old.
	started := *store.row.PendingSince
	require.NoError(t, service.recordSolutionHostBindingDesired(
		context.Background(), document, "", reason, true, now.Add(time.Hour)))
	require.Equal(t, started, *store.row.PendingSince)

	// And the envelope being fixed clears it, because a pass that withholds
	// nothing passes no reason.
	require.NoError(t, service.recordSolutionHostBindingDesired(
		context.Background(), document, "", "", true, now.Add(2*time.Hour)))
	require.Empty(t, store.row.PendingReason)
	require.Nil(t, store.row.PendingSince)
}

// An ADMISSION refusal is unchanged: it is the pending reason AND a refusal of
// the generation, which was not applied.
func TestAnAdmissionRefusalStaysARefusedGeneration(t *testing.T) {
	store := &desiredRowStore{}
	service := &Service{store: store}
	document := withheldRowDocument(t)

	require.NoError(t, service.recordSolutionHostBindingDesired(
		context.Background(), document, "stale generation", "", false, time.Now().UTC()))
	require.Equal(t, "stale generation", store.row.PendingReason)
	require.Len(t, store.decisions, 1)
	require.Equal(t, SolutionGenerationRefused, store.decisions[0].Decision)
	require.Equal(t, "stale generation", store.decisions[0].Reason)
}
