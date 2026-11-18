package infra

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"accounts/pkg/business"
)

// keysetPosition reads the page token of a listing ordered by (created_at, id)
// descending over a uuid id: the last row the previous page returned, as
// encodeAuditCursor wrote it. A token this host did not issue is the caller's
// error, never the first page; the id is parsed here so an unreadable one is
// refused before it reaches a uuid comparison and aborts the transaction.
func keysetPosition(pageToken string) (time.Time, string, error) {
	createdAt, id, err := decodeAuditCursor(pageToken)
	if err == nil {
		_, err = uuid.Parse(id)
	}
	if err != nil {
		return time.Time{}, "", business.NewStoreError(
			fmt.Errorf("page_token is not one this listing issued: %w", err), business.ErrTypeValidation)
	}
	return createdAt, id, nil
}
