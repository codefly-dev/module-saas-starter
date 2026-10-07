package business

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuditHistorySplitsBusySourceWindowsAndKeepsDigestStable(t *testing.T) {
	f := newHistoryFixture()
	f.source.rows = nil
	day := f.source.partitions[0].From.Add(time.Hour)
	for i := range 12 {
		row := historyRow(i+1, "", EventAuthLogin, day.Add(time.Duration(i)*time.Hour))
		row.Payload = map[string]any{"blob": strings.Repeat("x", 1000)}
		f.source.rows = append(f.source.rows, row)
	}
	report, err := f.copier(t, AuditHistoryCopyConfig{WindowBytes: 6000, BatchSize: 1}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Equal(t, 12, report.Partitions[0].Copied)
	for _, batch := range f.archive.batches {
		for _, record := range batch.Records {
			require.Nil(t, record.Entry.Payload, "retained records keep only canonical details")
		}
	}
	verify, err := f.copier(t, AuditHistoryCopyConfig{WindowBytes: 10000}).Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.NoError(t, err)
	require.Equal(t, report.PartitionsSHA256, verify.PartitionsSHA256, "window boundaries cannot alter the approved source digest")
	drop, err := f.copier(t, AuditHistoryCopyConfig{WindowBytes: 4000}).Run(context.Background(), AuditHistoryCopyOptions{
		VerifyOnly: true, ConfirmDrop: true, ExpectedPartitionsSHA256: verify.PartitionsSHA256,
	})
	require.NoError(t, err)
	require.Equal(t, report.PartitionsSHA256, drop.PartitionsSHA256)
	require.Equal(t, int64(3), drop.Dropped)
}

func TestAuditHistoryRejectsAnUnsplittablyDenseInstantBeforeWrites(t *testing.T) {
	f := newHistoryFixture()
	f.source.rows = nil
	at := f.source.partitions[0].From.Add(time.Hour)
	for i := range 8 {
		f.source.rows = append(f.source.rows, historyRow(i+1, "", EventAuthLogin, at))
	}
	report, err := f.copier(t, AuditHistoryCopyConfig{WindowBytes: 2000}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorIs(t, err, ErrAuditHistoryTooDense)
	require.False(t, report.Verified)
	require.Empty(t, f.archive.batches)
	require.Empty(t, f.source.drops)
}

func TestAuditHistoryVerifiesEveryDuplicateWithoutRetainingCopies(t *testing.T) {
	f := newHistoryFixture()
	f.source.rows = f.source.rows[:1]
	_, err := f.copier(t, AuditHistoryCopyConfig{}).Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	event := f.store.stored[0]
	for range 10000 {
		f.store.stored = append(f.store.stored, event)
	}
	report, err := f.copier(t, AuditHistoryCopyConfig{WindowBytes: 2000}).Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.NoError(t, err)
	require.True(t, report.Verified)
	f.store.stored[len(f.store.stored)-1].Details = "{\"corrupt\":true}"
	report, err = f.copier(t, AuditHistoryCopyConfig{WindowBytes: 2000}).Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.ErrorIs(t, err, ErrAuditHistoryUnverified)
	require.Contains(t, report.Partitions[0].Problems[0], "stored details do not hash")
}

type streamingHistorySource struct {
	*historySource
	streams int
}

func (s *streamingHistorySource) ReadAuditHistory(context.Context, time.Time, time.Time, *AuditHistoryCursor, int) ([]AuditEntry, error) {
	panic("the streaming copy must never construct a source page")
}

func (s *streamingHistorySource) StreamAuditHistory(ctx context.Context, from, to time.Time, _ int64, visit func(AuditEntry) error) error {
	s.streams++
	rows, err := s.historySource.ReadAuditHistory(ctx, from, to, nil, len(s.rows)+1)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

func TestAuditHistoryUsesTheStreamingSource(t *testing.T) {
	f := newHistoryFixture()
	c := f.copier(t, AuditHistoryCopyConfig{})
	source := &streamingHistorySource{historySource: f.source}
	c.cfg.Source = source
	report, err := c.Run(context.Background(), AuditHistoryCopyOptions{})
	require.NoError(t, err)
	require.True(t, report.Verified)
	require.Positive(t, source.streams)
}

type historyOverflowAfterWrite struct {
	*historyStore
	reads int
}

func (s *historyOverflowAfterWrite) ReadStoredAuditEvents(ctx context.Context, from, to time.Time, visit func(StoredAuditEvent) error) error {
	s.reads++
	if s.reads == 2 {
		return ErrAuditHistoryWindowTooLarge
	}
	return s.historyStore.ReadStoredAuditEvents(ctx, from, to, visit)
}

func TestAuditHistoryPostWriteOverflowStopsWithoutRepeatingRepair(t *testing.T) {
	f := newHistoryFixture()
	f.source.rows = f.source.rows[:1]
	c := f.copier(t, AuditHistoryCopyConfig{})
	reader := &historyOverflowAfterWrite{historyStore: f.store}
	c.cfg.ReadBack = reader
	report, err := c.Run(context.Background(), AuditHistoryCopyOptions{})
	require.ErrorIs(t, err, ErrAuditHistoryWindowTooLarge)
	require.False(t, report.Verified)
	require.Equal(t, 2, reader.reads, "no smaller read after writes in this run")
	require.Equal(t, 1, f.store.appends)
	require.Empty(t, f.source.drops)
	report, err = c.Run(context.Background(), AuditHistoryCopyOptions{VerifyOnly: true})
	require.NoError(t, err)
	require.True(t, report.Verified)
}
