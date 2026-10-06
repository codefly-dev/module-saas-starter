package bigquerystore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/business"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

// What AppendAuditBatch says about a failure is what the relay acts on: it sets
// a row aside only on a business.PermanentRowRejection, and retries everything
// else. These pin which BigQuery answers become one.

// scriptedInserter answers each request with answer(request), nil to accept it.
type scriptedInserter struct {
	answer   func(request []Row) error
	requests [][]Row
}

func (s *scriptedInserter) Put(_ context.Context, src any) error {
	savers := src.([]bigquery.ValueSaver)
	request := make([]Row, len(savers))
	for i, saver := range savers {
		request[i] = saver.(Row)
	}
	s.requests = append(s.requests, request)
	if s.answer == nil {
		return nil
	}
	return s.answer(request)
}

// insertErrors is BigQuery's insertAll answer for a request: row i of the
// request is refused with reasons[i] (the empty reason is a row it accepted).
func insertErrors(request []Row, reasons map[int]string) error {
	var multi bigquery.PutMultiError
	for i, row := range request {
		reason, listed := reasons[i]
		if !listed {
			continue
		}
		multi = append(multi, bigquery.RowInsertionError{
			InsertID: row.InsertID, RowIndex: i,
			Errors: bigquery.MultiError{&bigquery.Error{Reason: reason, Location: "occurred_at", Message: "cannot parse " + reason}},
		})
	}
	return multi
}

func refusedIDs(err error) []string {
	var ids []string
	for _, rejection := range business.PermanentRowRejections(err) {
		ids = append(ids, rejection.EventID)
	}
	return ids
}

func batchOf(t *testing.T, n int, class business.AuditRetentionClass) ([]business.AuditRecord, []string) {
	t.Helper()
	records := make([]business.AuditRecord, n)
	ids := make([]string, n)
	for i := range records {
		records[i] = record(t, class, "")
		ids[i] = records[i].Entry.ID
	}
	return records, ids
}

func TestARowBigQueryRefusesAsInvalidIsARefusalOfThatRowAlone(t *testing.T) {
	records, ids := batchOf(t, 6, business.RetentionSecurity)
	events := &scriptedInserter{answer: func(request []Row) error {
		// BigQuery refuses rows 1 and 4 as invalid and stops the rest of the request.
		reasons := map[int]string{}
		for i := range request {
			reasons[i] = "stopped"
		}
		reasons[1], reasons[4] = "invalid", "invalid"
		return insertErrors(request, reasons)
	}}
	store := &Store{events: events, details: &scriptedInserter{}}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Error(t, err)
	require.Equal(t, []string{ids[1], ids[4]}, refusedIDs(err), "the rows refused as invalid, and not the rows that were stopped")
	require.ErrorContains(t, err, "stopped", "the failure of the others stays in the error, for the log")
}

func TestOnlyTheReasonInvalidIsAPermanentRefusal(t *testing.T) {
	records, _ := batchOf(t, 3, business.RetentionSecurity)
	for _, reason := range []string{"stopped", "backendError", "internalError", "rateLimitExceeded", "quotaExceeded", "timeout", "notFound", "accessDenied", "", "invalidQuery"} {
		events := &scriptedInserter{answer: func(request []Row) error {
			return insertErrors(request, map[int]string{0: reason, 1: reason, 2: reason})
		}}
		store := &Store{events: events, details: &scriptedInserter{}}

		err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

		require.Error(t, err, reason)
		require.Empty(t, refusedIDs(err), "a row BigQuery gives the reason %q is not refused for its content", reason)
	}
}

func TestAnInvalidRowBesideARowThatFailedForAnotherReasonIsStillRefused(t *testing.T) {
	records, ids := batchOf(t, 3, business.RetentionSecurity)
	events := &scriptedInserter{answer: func(request []Row) error {
		return insertErrors(request, map[int]string{0: "backendError", 1: "invalid", 2: "stopped"})
	}}
	store := &Store{events: events, details: &scriptedInserter{}}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, []string{ids[1]}, refusedIDs(err))
}

func TestAFailureOfTheRequestIsNeverARefusalOfARow(t *testing.T) {
	records, _ := batchOf(t, 4, business.RetentionContent)
	for name, failure := range map[string]error{
		"unavailable": &googleapi.Error{Code: 503, Message: "backendError"},
		"throttled":   &googleapi.Error{Code: 429, Message: "rateLimitExceeded"},
		"quota":       &googleapi.Error{Code: 403, Message: "quotaExceeded", Errors: []googleapi.ErrorItem{{Reason: "quotaExceeded"}}},
		"forbidden":   &googleapi.Error{Code: 403, Message: "accessDenied"},
		"bad request": &googleapi.Error{Code: 400, Message: "invalid"},
		"not found":   &googleapi.Error{Code: 404, Message: "notFound"},
		"deadline":    context.DeadlineExceeded,
		"transport":   errors.New("write tcp 10.0.0.1:443: connection reset by peer"),
		"unknown":     errors.New("an answer of a kind this store does not know"),
		"empty multi": bigquery.PutMultiError{},
	} {
		for _, table := range []string{"events", "details"} {
			events, details := &scriptedInserter{}, &scriptedInserter{}
			if table == "events" {
				events.answer = func([]Row) error { return failure }
			} else {
				details.answer = func([]Row) error { return failure }
			}
			store := &Store{events: events, details: details}

			err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

			require.Error(t, err, name)
			require.Empty(t, refusedIDs(err), "%s, %s table: a failure of the request says nothing about a row", name, table)
			if name != "empty multi" {
				require.ErrorIs(t, err, failure, "%s: the failure is returned as it came", name)
			}
		}
	}
}

func TestARefusalInThePutErrorSurvivesWrapping(t *testing.T) {
	records, ids := batchOf(t, 2, business.RetentionSecurity)
	events := &scriptedInserter{answer: func(request []Row) error {
		return fmt.Errorf("transport wrapper: %w", insertErrors(request, map[int]string{1: "invalid"}))
	}}
	store := &Store{events: events, details: &scriptedInserter{}}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, []string{ids[1]}, refusedIDs(err))
}

func TestARowRefusedInTheDetailsTableIsARefusalOfItsEvent(t *testing.T) {
	records, ids := batchOf(t, 3, business.RetentionContent)
	details := &scriptedInserter{answer: func(request []Row) error {
		return insertErrors(request, map[int]string{2: "invalid"})
	}}
	events := &scriptedInserter{}
	store := &Store{events: events, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, []string{ids[2]}, refusedIDs(err))
	require.ErrorContains(t, err, DetailsTable)
}

func TestEveryRowThatCannotFitARequestIsARefusalAndNothingIsSent(t *testing.T) {
	events, details := &scriptedInserter{}, &scriptedInserter{}
	store := &Store{events: events, details: details}
	small, hugeOne, hugeTwo := largeRecord(t, 1<<10), largeRecord(t, maxRequestBytes), largeRecord(t, maxRequestBytes)

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: []business.AuditRecord{hugeOne, small, hugeTwo},
	})

	require.Equal(t, []string{hugeOne.Entry.ID, hugeTwo.Entry.ID}, refusedIDs(err), "every such row, not only the first")
	require.ErrorIs(t, err, ErrRowTooLarge)
	var tooLarge *RowTooLargeError
	require.ErrorAs(t, err, &tooLarge)
	require.Empty(t, events.requests)
	require.Empty(t, details.requests)
}
