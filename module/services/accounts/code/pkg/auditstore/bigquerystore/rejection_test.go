package bigquerystore

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	require.Empty(t, events.requests, "no events row is written when a details row is refused: it would be an event with an empty payload")
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

// orderedInserter is a scriptedInserter that records, in a log shared with the
// other table's, which table each request went to.
type orderedInserter struct {
	scriptedInserter
	table string
	log   *[]string
}

func (o *orderedInserter) Put(ctx context.Context, src any) error {
	*o.log = append(*o.log, o.table)
	return o.scriptedInserter.Put(ctx, src)
}

func (s *scriptedInserter) ids() []string {
	var ids []string
	for _, request := range s.requests {
		for _, row := range request {
			ids = append(ids, row.InsertID)
		}
	}
	return ids
}

func TestTheDetailsAreWrittenBeforeTheEventsTheyBelongTo(t *testing.T) {
	var log []string
	events := &orderedInserter{table: EventsTable, log: &log}
	details := &orderedInserter{table: DetailsTable, log: &log}
	store := &Store{events: events, details: details}
	records, ids := batchOf(t, 3, business.RetentionContent)
	security := record(t, business.RetentionSecurity, "")

	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: append(records, security),
	}))

	require.Equal(t, []string{DetailsTable, EventsTable}, log, "a content event's events row is written only once its details are accepted")
	require.Equal(t, ids, details.ids())
	require.Equal(t, append(slices.Clone(ids), security.Entry.ID), events.ids(), "and every event, the security one included, then has its events row")
}

// A details row BigQuery refuses leaves no events row of its event: the events
// table is where every read starts, and an event there with no details is an
// event with an empty payload whose quarantine reason says the warehouse holds
// nothing of it.
func TestARefusedDetailsRowLeavesNoEventsRowOfItsEvent(t *testing.T) {
	records, ids := batchOf(t, 4, business.RetentionContent)
	security := record(t, business.RetentionSecurity, "")
	details := &scriptedInserter{answer: func(request []Row) error {
		reasons := map[int]string{}
		for i := range request {
			reasons[i] = "stopped"
		}
		reasons[1] = "invalid"
		return insertErrors(request, reasons)
	}}
	events := &scriptedInserter{}
	store := &Store{events: events, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: append(records, security),
	})

	require.Equal(t, []string{ids[1]}, refusedIDs(err))
	require.Empty(t, events.requests, "nothing of the batch reaches the events table until its details are all accepted")

	// The relay sends the rest again without the refused row, and every events row
	// of it is then written, security-class ones included.
	details.answer = nil
	remaining := append(slices.Clone(records[:1]), records[2:]...)
	require.NoError(t, store.AppendAuditBatch(context.Background(), business.AuditBatch{
		DeploymentID: "deployment-1", Records: append(remaining, security),
	}))
	require.NotContains(t, events.ids(), ids[1], "the refused event never has an events row")
	require.Len(t, events.ids(), 4)
}

// A table changed under the writer refuses every row alike. That is not a set of
// bad rows, and naming each of them would have the relay set the whole stream
// aside.
func TestARequestEveryRowOfWhichIsRefusedForOneReasonNamesNoRow(t *testing.T) {
	for _, table := range []string{"details", "events"} {
		records, _ := batchOf(t, 6, business.RetentionContent)
		refuse := &scriptedInserter{answer: func(request []Row) error {
			reasons := map[int]string{}
			for i := range request {
				reasons[i] = "invalid"
			}
			return insertErrors(request, reasons)
		}}
		events, details := &scriptedInserter{}, &scriptedInserter{}
		if table == "events" {
			events = refuse
		} else {
			details = refuse
		}
		store := &Store{events: events, details: details}

		err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

		require.Error(t, err, table)
		require.Empty(t, refusedIDs(err), "%s: no row is named, so the relay retries the batch and sets nothing aside", table)
		require.ErrorContains(t, err, "every one of the 6 rows of a request was refused as invalid for one reason", table)
		require.ErrorContains(t, err, "cannot parse invalid", "%s: the reason BigQuery gave is kept", table)
	}
}

func TestARequestRefusedRowByRowForDifferentReasonsNamesEachRow(t *testing.T) {
	records, ids := batchOf(t, 3, business.RetentionContent)
	details := &scriptedInserter{answer: func(request []Row) error {
		var multi bigquery.PutMultiError
		for i, row := range request {
			multi = append(multi, bigquery.RowInsertionError{
				InsertID: row.InsertID, RowIndex: i,
				Errors: bigquery.MultiError{&bigquery.Error{Reason: "invalid", Location: "details", Message: "invalid value " + row.InsertID}},
			})
		}
		return multi
	}}
	store := &Store{events: &scriptedInserter{}, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, ids, refusedIDs(err), "each row has its own reason, so each row is the one at fault")
}

func TestOneRowRefusedAloneIsARefusalOfThatRowWhateverTheReason(t *testing.T) {
	records, ids := batchOf(t, 1, business.RetentionContent)
	details := &scriptedInserter{answer: func(request []Row) error {
		return insertErrors(request, map[int]string{0: "invalid"})
	}}
	store := &Store{events: &scriptedInserter{}, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	require.Equal(t, ids, refusedIDs(err), "a request of one row cannot be told from a table that refuses everything")
}

// The reason is what BigQuery said of the row and nothing of where the row sat in
// the request, so that two rows refused for one cause have one reason.
func TestTheReasonOfARefusedRowIsTheSameForRowsRefusedForOneCause(t *testing.T) {
	records, _ := batchOf(t, 5, business.RetentionContent)
	details := &scriptedInserter{answer: func(request []Row) error {
		reasons := map[int]string{}
		for i := range request {
			reasons[i] = "stopped"
		}
		reasons[1], reasons[3] = "invalid", "invalid"
		return insertErrors(request, reasons)
	}}
	store := &Store{events: &scriptedInserter{}, details: details}

	err := store.AppendAuditBatch(context.Background(), business.AuditBatch{DeploymentID: "deployment-1", Records: records})

	rejections := business.PermanentRowRejections(err)
	require.Len(t, rejections, 2)
	require.NotEmpty(t, rejections[0].Reason)
	require.Equal(t, rejections[0].Reason, rejections[1].Reason)
	require.NotContains(t, rejections[0].Reason, rejections[0].EventID)
	require.NotContains(t, rejections[0].Reason, "index")
}
