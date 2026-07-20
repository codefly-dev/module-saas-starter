package framework

import (
	"context"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// Plugin defines the interface that all service plugins must implement.
// Descriptor-derived registration owns gRPC; plugins contribute selected REST
// handlers and database migrations.
type Plugin interface {
	// Name returns a unique identifier for this plugin.
	Name() string

	// RegisterREST registers the plugin's REST gateway handlers.
	RegisterREST(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error

	// Migrations returns an ordered list of SQL migration file paths.
	// Return nil if the plugin has no migrations.
	Migrations() []string
}
