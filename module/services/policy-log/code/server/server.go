// Package server is the gRPC projection of the witness. It owns the wire shape
// and the translation of the witness error taxonomy, and no policy of its own:
// validation, sequencing, signing and the five-second budget all stay behind
// the transport-independent service boundary.
package server

import (
	"context"
	"errors"
	"fmt"

	policylogv1 "policy-log/pkg/gen/saas/policylog/v1"
	"policy-log/service"
	"policy-log/witness"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Witness is the boundary this server projects. It is an interface so that each
// arm of the error taxonomy can be exercised exactly, rather than approximated
// by corrupting a store; *service.Service is the only implementation that ships.
type Witness interface {
	Append(context.Context, witness.Entry) (*witness.Receipt, error)
	Cursor(context.Context) (*witness.Cursor, error)
	Read(context.Context, uint64, uint32) ([]witness.Record, error)
	Keys() witness.Key
}

// The production implementation must keep satisfying the boundary.
var _ Witness = (*service.Service)(nil)

type Server struct {
	policylogv1.UnimplementedWitnessServiceServer
	witness Witness
	failed  func(error)
}

// New requires a failure reporter because an error outside the taxonomy is
// deliberately not echoed to the caller: a store error can carry bucket
// identity and endpoint detail, and the caller's correct behaviour does not
// depend on it. The detail goes to this service's own log instead.
func New(w Witness, failed func(error)) (*Server, error) {
	if w == nil || failed == nil {
		return nil, errors.New("policy-log: witness boundary and failure reporter are required")
	}
	return &Server{witness: w, failed: failed}, nil
}

// unreachable is the only thing a caller learns about an error the taxonomy does
// not name. It says what the caller needs: no sequence was assigned and no
// receipt exists, so the identical operation may be retried.
const unreachable = "policy log could not reach its authority store; no sequence was assigned and no receipt was issued"

// statusFor maps the witness taxonomy onto gRPC codes. The five taxonomy errors
// are NOT interchangeable and must never collapse into one code:
//
//   - ErrInvalid     -> InvalidArgument.    The request is malformed. Retrying
//     the same bytes can never succeed.
//   - ErrConflict    -> AlreadyExists.      The operation id already names a
//     DIFFERENT entry. Permanent: the caller
//     must not retry, and must not rename the
//     operation to get past it.
//   - ErrPending     -> FailedPrecondition. Another operation holds the next
//     sequence uncommitted. Only an explicit
//     retry of THAT operation can clear it, so
//     this caller must not retry until the log's
//     state is repaired. Distinct from
//     ErrConflict because this one does clear.
//   - ErrRollback    -> Aborted.            The head moved backwards, changed a
//     known hash, or the caller is ahead of the
//     log. A sequencer check failed; the caller
//     must stop serving the authority it holds,
//     not retry.
//   - ErrIntegrity   -> DataLoss.           The log's own stored evidence does
//     not verify. Unrecoverable corruption,
//     distinct from a regression.
//
// Cancellation and the deadline keep their own codes, because both leave the
// outcome genuinely ambiguous: a storage request already in flight can have
// committed. The caller reconciles by retrying the identical operation, whose
// receipt the head will return if it did.
func (s *Server) statusFor(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, witness.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, witness.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, witness.ErrPending):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, witness.ErrRollback):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, witness.ErrIntegrity):
		return status.Error(codes.DataLoss, err.Error())
	default:
		s.failed(err)
		return status.Error(codes.Unavailable, unreachable)
	}
}

// entryFrom carries the wire entry into the contract without inventing any part
// of it.
//
// A missing or out-of-range requested_at is refused here rather than passed on:
// AsTime of an absent Timestamp is 1970-01-01, which Entry.Validate accepts, so
// the log would sign an entry hash over a timestamp the caller never sent — and
// no retry of that operation could ever reproduce it.
func entryFrom(wire *policylogv1.Entry) (witness.Entry, error) {
	if wire == nil {
		return witness.Entry{}, fmt.Errorf("%w: entry is absent", witness.ErrInvalid)
	}
	if err := wire.GetRequestedAt().CheckValid(); err != nil {
		return witness.Entry{}, fmt.Errorf("%w: requested_at is required and must be a valid timestamp", witness.ErrInvalid)
	}
	return witness.Entry{
		OperationID:   wire.GetOperationId(),
		Kind:          wire.GetKind(),
		Subject:       wire.GetSubject(),
		Tenant:        wire.GetTenant(),
		NarrowingHash: wire.GetNarrowingHash(),
		RequestedAt:   wire.GetRequestedAt().AsTime(),
	}, nil
}

// entryTo and receiptTo carry raw digests as bytes and the timestamps as
// seconds and nanoseconds, which is exactly what the canonical encodings hash.
// A reader can therefore recompute entry_hash from the wire entry it received.
func entryTo(entry witness.Entry) *policylogv1.Entry {
	return &policylogv1.Entry{
		OperationId:   entry.OperationID,
		Kind:          entry.Kind,
		Subject:       entry.Subject,
		Tenant:        entry.Tenant,
		NarrowingHash: entry.NarrowingHash,
		RequestedAt:   timestamppb.New(entry.RequestedAt),
	}
}

func receiptTo(receipt witness.Receipt) *policylogv1.Receipt {
	return &policylogv1.Receipt{
		Seq:       receipt.Seq,
		EntryHash: receipt.EntryHash,
		PrevHash:  receipt.PrevHash,
		ChainHash: receipt.ChainHash,
		IssuedAt:  timestamppb.New(receipt.IssuedAt),
		Signature: receipt.Signature,
	}
}

func (s *Server) Append(ctx context.Context, request *policylogv1.AppendRequest) (*policylogv1.AppendResponse, error) {
	entry, err := entryFrom(request.GetEntry())
	if err != nil {
		return nil, s.statusFor(err)
	}
	receipt, err := s.witness.Append(ctx, entry)
	if err != nil {
		return nil, s.statusFor(err)
	}
	return &policylogv1.AppendResponse{Receipt: receiptTo(*receipt)}, nil
}

func (s *Server) Cursor(ctx context.Context, _ *policylogv1.CursorRequest) (*policylogv1.CursorResponse, error) {
	cursor, err := s.witness.Cursor(ctx)
	if err != nil {
		return nil, s.statusFor(err)
	}
	return &policylogv1.CursorResponse{Cursor: &policylogv1.Cursor{
		HeadSeq:   cursor.HeadSeq,
		ChainHash: cursor.ChainHash,
		IssuedAt:  timestamppb.New(cursor.IssuedAt),
		Signature: cursor.Signature,
	}}, nil
}

func (s *Server) Read(ctx context.Context, request *policylogv1.ReadRequest) (*policylogv1.ReadResponse, error) {
	records, err := s.witness.Read(ctx, request.GetFromSeq(), request.GetLimit())
	if err != nil {
		return nil, s.statusFor(err)
	}
	// An empty page is a page: reading at head+1 returns one, and a nil slice
	// here must not read as a different answer from an explicitly empty list.
	response := &policylogv1.ReadResponse{Records: make([]*policylogv1.Record, 0, len(records))}
	for _, record := range records {
		response.Records = append(response.Records, &policylogv1.Record{
			Entry:   entryTo(record.Entry),
			Receipt: receiptTo(record.Receipt),
		})
	}
	return response, nil
}

func (s *Server) Keys(_ context.Context, _ *policylogv1.KeysRequest) (*policylogv1.KeysResponse, error) {
	key := s.witness.Keys()
	return &policylogv1.KeysResponse{Key: &policylogv1.Key{
		Algorithm: key.Algorithm,
		KeyId:     key.KeyID,
		PublicKey: key.PublicKey,
	}}, nil
}
