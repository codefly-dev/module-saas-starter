package bigquerystore

import (
	"context"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auditstore/bigqueryfake"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestHistoryDetailsAreBoundedBeforeRetainingTheWindow(t *testing.T) {
	w := newBoundWarehouse(t)
	at := boundNow.Add(-time.Hour)
	w.append(t, instant(t, 8, at)...)
	reader := w.reader(t, 2048, 0)
	visited := 0
	err := reader.ReadStoredAuditEvents(t.Context(), at.Add(-time.Second), at.Add(time.Second), func(business.StoredAuditEvent) error {
		visited++
		return nil
	})
	require.ErrorIs(t, err, business.ErrAuditHistoryWindowTooLarge)
	require.Zero(t, visited, "the caller can retry a smaller window before accepting any events")
}

func TestHistoryIdenticalDetailsCopiesFitAndCorruptCopiesRemainVisible(t *testing.T) {
	w := newBoundWarehouse(t)
	at := boundNow.Add(-time.Hour)
	record := boundEvent(t, 1, at, map[string]any{"pad": strings.Repeat("p", 500)})
	w.append(t, record)
	for i := 0; i < 100; i++ {
		w.insertDetails(t, record, record.Details)
	}
	reader := w.reader(t, 4096, 0)
	read := func() business.StoredAuditEvent {
		var got business.StoredAuditEvent
		require.NoError(t, reader.ReadStoredAuditEvents(context.Background(), at.Add(-time.Second), at.Add(time.Second), func(event business.StoredAuditEvent) error {
			got = event
			return nil
		}))
		return got
	}
	require.Equal(t, record.Details, read().Details)
	// The claimed hash is unchanged. Byte-identical redeliveries can be
	// discarded, but a different text under that hash must remain observable.
	forged := DetailRow(boundDeployment, record)
	forged.Values["details"] = `{"pad":"corrupt"}`
	require.NoError(t, w.fake.Insert(bigqueryfake.TablePath(boundProject, boundDataset, DetailsTable), forged.Values))
	w.insertDetails(t, record, record.Details)
	err := reader.ReadStoredAuditEvents(t.Context(), at.Add(-time.Second), at.Add(time.Second), func(business.StoredAuditEvent) error { return nil })
	require.ErrorContains(t, err, "do not match their stored hash")
}

func TestHistoryPropagatesTheCallersWindowBudgetSignal(t *testing.T) {
	w := newBoundWarehouse(t)
	at := boundNow.Add(-time.Hour)
	w.append(t, boundEvent(t, 1, at, nil))
	err := w.reader(t, 4096, 0).ReadStoredAuditEvents(t.Context(), at.Add(-time.Second), at.Add(time.Second), func(business.StoredAuditEvent) error {
		return business.ErrAuditHistoryWindowTooLarge
	})
	require.ErrorIs(t, err, business.ErrAuditHistoryWindowTooLarge)
}
