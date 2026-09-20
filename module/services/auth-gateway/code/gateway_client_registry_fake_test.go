package main

import (
	"context"
	"sync"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
)

// fakeClientRegistry stands in for accounts in gateway tests. The real registry
// is process configuration accounts validates at startup, so there are no
// write paths to mirror here — a test states the registered set and, where it
// exercises the failure path, the error a read returns.
type fakeClientRegistry struct {
	mu      sync.Mutex
	clients []*accountsv1.RegisteredClient
	listErr error

	listCalls int
}

func newFakeClientRegistry(clients ...*accountsv1.RegisteredClient) *fakeClientRegistry {
	return &fakeClientRegistry{clients: clients}
}

func registeredClient(id string, origins ...string) *accountsv1.RegisteredClient {
	return &accountsv1.RegisteredClient{
		ClientId: id,
		Name:     id,
		Kind:     accountsv1.ClientKind_CLIENT_KIND_PUBLIC,
		Origins:  origins,
	}
}

func (f *fakeClientRegistry) List(
	_ context.Context, _ *accountsv1.ListRegisteredClientsRequest,
) (*accountsv1.ListRegisteredClientsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &accountsv1.ListRegisteredClientsResponse{Clients: f.clients}, nil
}

func (f *fakeClientRegistry) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

func (f *fakeClientRegistry) setClients(clients ...*accountsv1.RegisteredClient) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients = clients
}
