package email

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Factory builds a configured Sender. Implementations validate their own
// secrets and fail closed rather than returning a degraded sender.
type Factory func(ctx context.Context) (Sender, error)

// Registry maps a provider name to its factory. It is an ordinary value built
// and populated at startup; adding a provider is one Register call rather than
// a new switch case.
type Registry struct {
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory under a case-insensitive name. A duplicate name
// replaces the earlier registration.
func (r *Registry) Register(name string, factory Factory) {
	r.factories[strings.ToLower(strings.TrimSpace(name))] = factory
}

// Select builds the provider registered under name. An unknown name fails
// closed, naming the registered providers.
func (r *Registry) Select(ctx context.Context, name string) (Sender, error) {
	factory, ok := r.factories[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("EMAIL_PROVIDER must be one of: %s", strings.Join(r.names(), ", "))
	}
	return factory(ctx)
}

func (r *Registry) names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
