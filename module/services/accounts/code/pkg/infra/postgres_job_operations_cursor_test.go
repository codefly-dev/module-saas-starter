package infra

import (
	"errors"
	"testing"
	"time"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestJobPageTokenRoundTripAndRejectsUntrustedInput(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 123).UTC()
	want := &jobsv1.JobSummary{
		Id: uuid.NewString(), CreatedAt: timestamppb.New(createdAt),
	}
	token, err := encodeJobPageToken(want)
	if err != nil {
		t.Fatalf("encodeJobPageToken() error = %v", err)
	}
	cursor, err := decodeJobPageToken(token)
	if err != nil {
		t.Fatalf("decodeJobPageToken() error = %v", err)
	}
	if cursor.ID != want.GetId() || !cursor.CreatedAt.Equal(createdAt) {
		t.Fatalf("cursor = %+v, want id=%s created_at=%s", cursor, want.GetId(), createdAt)
	}

	for _, invalid := range []string{"not-base64!", "e30", token + "garbage"} {
		if _, err := decodeJobPageToken(invalid); !errors.Is(err, jobs.ErrInvalidPageToken) {
			t.Fatalf("decodeJobPageToken(%q) error = %v, want ErrInvalidPageToken", invalid, err)
		}
	}
}
