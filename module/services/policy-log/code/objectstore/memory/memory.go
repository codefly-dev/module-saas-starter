// Package memory supplies the generation-aware fake used by policy-log tests.
// The served process never imports it.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"policy-log/objectstore"
)

type Store struct {
	mu         sync.Mutex
	objects    map[string]objectstore.Object
	generation int64
}

func New() *Store { return &Store{objects: make(map[string]objectstore.Object)} }

func (s *Store) Read(ctx context.Context, name string) (objectstore.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	obj, ok := s.objects[name]
	if !ok {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	obj.Data = bytes.Clone(obj.Data)
	return obj, nil
}

func (s *Store) Write(ctx context.Context, name string, data []byte, generation int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if generation < 0 || len(data) > objectstore.MaxObjectBytes {
		return 0, fmt.Errorf("invalid object write")
	}
	obj, exists := s.objects[name]
	if (generation == 0 && exists) || (generation > 0 && (!exists || obj.Generation != generation)) {
		return 0, objectstore.ErrPrecondition
	}
	s.generation++
	s.objects[name] = objectstore.Object{Data: bytes.Clone(data), Generation: s.generation}
	return s.generation, nil
}
