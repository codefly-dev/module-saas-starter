// Package service joins the witness and its best-effort warehouse mirror.
// Transport adapters must use this boundary rather than offering candidates to
// the mirror themselves: only a committed Append result may be mirrored.
package service

import (
	"bytes"
	"context"
	"errors"

	"policy-log/witness"
)

type Service struct {
	log    *witness.Log
	mirror Mirror
}

// Mirror must offer without blocking; the production mirror.Worker has a
// bounded queue and drops a full queue instead of waiting on warehouse I/O.
type Mirror interface{ Offer(witness.Record) }

func New(log *witness.Log, worker Mirror) (*Service, error) {
	if log == nil || worker == nil {
		return nil, errors.New("policy-log: witness and warehouse mirror are required")
	}
	return &Service{log: log, mirror: worker}, nil
}

func (s *Service) Append(ctx context.Context, entry witness.Entry) (*witness.Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, witness.AppendDeadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	entry.NarrowingHash = bytes.Clone(entry.NarrowingHash)
	receipt, err := s.log.Append(ctx, entry)
	if err != nil {
		return nil, err
	}
	s.mirror.Offer(witness.Record{Entry: entry, Receipt: *receipt})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (s *Service) Cursor(ctx context.Context) (*witness.Cursor, error) { return s.log.Cursor(ctx) }
func (s *Service) Read(ctx context.Context, fromSeq uint64, limit uint32) ([]witness.Record, error) {
	return s.log.Read(ctx, fromSeq, limit)
}
func (s *Service) Keys() witness.Key { return s.log.Keys() }
