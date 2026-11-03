package objectstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The host's own writes to the composed object-storage service.
//
// Every other call in this package carries a per-call Binding: a tenant's
// bucket and the credential for it, for one source, read-only. These do not.
// `PutRequest` carries no Binding field at all, so a Put always lands in the
// object-storage service's own configured bucket under its own prefix — the
// bucket the composition points that service at, not one accounts names. That
// is exactly right for a host-owned archive: the key is accounts' to choose,
// while the bucket, its credentials and its retention posture stay with the
// composition and with whoever operates it.
//
// Nothing here reads. The host writes an archive object and never needs it
// back; reading it is an auditor's job, through the store, with the store's own
// access control.

// ErrObjectExists reports a create-if-absent write the store refused because
// the key is already there. It is a successful outcome for an idempotent
// archiver — the day is already archived — and a refusal for anything that
// meant to overwrite, so it is a named error rather than a silent success.
var ErrObjectExists = errors.New("objectstore: the object already exists")

// putChunk bounds one streamed data frame. The service's own limit is larger;
// this keeps a single gRPC message modest for a multi-megabyte archive.
const putChunk = 1 << 20

// writeTimeout bounds one archive write. A day's audit log is a bounded body
// and the archiver holds a job lease while this runs, so a store that accepts
// the stream and then stalls must not hold the lease indefinitely.
const writeTimeout = 2 * time.Minute

// HostStore writes objects to the object-storage service's own bucket.
type HostStore struct {
	client storagev0.ObjectStorageClient
}

// NewHostStore binds the host's writes to client.
func NewHostStore(client storagev0.ObjectStorageClient) *HostStore {
	return &HostStore{client: client}
}

// PutIfAbsent writes body at key and refuses, with ErrObjectExists, if the key
// is already taken. The refusal is the store's own (`if_none_match: "*"`),
// decided where the object is, not by a read-then-write here that two runs
// could interleave inside.
//
// It is the write an archive wants: an object that was put once can never be
// quietly replaced by a later run with different bytes, whatever the bucket's
// retention posture is — which, until the lock exists, is the only thing
// standing between an archived day and a rewritten one.
func (s *HostStore) PutIfAbsent(ctx context.Context, key, contentType string, body []byte) (etag string, err error) {
	if s == nil || s.client == nil {
		return "", errors.New("objectstore: no object-storage service is composed")
	}
	if key == "" {
		return "", errors.New("objectstore: a key is required")
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	stream, err := s.client.Put(ctx)
	if err != nil {
		return "", putError(err)
	}
	header := &storagev0.PutRequest{Kind: &storagev0.PutRequest_Header{Header: &storagev0.PutHeader{
		Key:          key,
		ContentType:  contentType,
		IfNoneMatch:  "*",
		TotalSize:    int64(len(body)),
		CacheControl: "no-store",
	}}}
	// A Send that fails means the server already closed the stream with its
	// real status, and that status — "the object exists", "the bucket refuses
	// this key" — is on CloseAndRecv, not on Send, which reports only io.EOF.
	// So a send failure stops the loop and the answer comes from CloseAndRecv
	// either way; returning the Send error would turn every store refusal into
	// an unexplained EOF.
	sent := stream.Send(header) == nil
	for offset := 0; sent && offset < len(body); offset += putChunk {
		end := min(offset+putChunk, len(body))
		sent = stream.Send(&storagev0.PutRequest{
			Kind: &storagev0.PutRequest_Data{Data: body[offset:end]},
		}) == nil
	}
	result, err := stream.CloseAndRecv()
	if err != nil {
		return "", putError(err)
	}
	return result.GetEtag(), nil
}

// putError maps the store's refusals. A create-if-absent collision is
// ErrObjectExists whichever code the backend reports it under — S3 answers 412
// and GCS 409, and the service forwards the condition rather than normalising
// it — so every code that can carry one is recognised.
func putError(err error) error {
	switch status.Code(err) {
	case codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted:
		return fmt.Errorf("%w: %w", ErrObjectExists, err)
	}
	return storeError(err)
}
