package main

// STOPGAP (codefly-dev/service-go-grpc#223): this entrypoint should be the
// go-grpc agent's generated output, and it is hand-maintained only until the
// agent can generate it for this service's shape. The agent's main-only
// refresh would drop the module authority listener declared in
// service.codefly.yaml (it resolves one listener per API), and it would start
// the server before work's wiring completes. So this file carries no
// generated-code marker, and the agent's Sync leaves it alone, as it leaves
// this service's adapters and plugins/registry.go. Restore the marker and
// adopt the generated main when #223 lands. Service logic goes in work.go,
// registered with WithWork.

import (
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/standards"
	"github.com/codefly-dev/core/wool"

	codefly "github.com/codefly-dev/sdk-go"
)

type Clean func()
type Work func(ctx context.Context) (Clean, error)

var work Work

func WithWork(w Work) {
	work = w
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	provider, err := codefly.Init(ctx)
	if err != nil {
		panic(err)
	}
	ctx = provider.Inject(ctx)

	defer codefly.CatchPanic(ctx)

	config := &adapters.Configuration{
		EndpointGrpcPort: codefly.For(ctx).API(standards.GRPC).NetworkInstance().Port,
	}
	if net := codefly.For(ctx).API(standards.REST).NetworkInstance(); net != nil {
		config.EndpointHttpPort = shared.Pointer(net.Port)
	}
	if net := codefly.For(ctx).API(standards.CONNECT).NetworkInstance(); net != nil {
		config.EndpointConnectPort = shared.Pointer(net.Port)
	}
	// The module authority endpoint is declared in service.codefly.yaml, and a
	// topology that omits it — or gives it a port another listener already
	// binds — is refused by the deployment generator, so a COMPOSITION error is
	// caught at render (cataloggen.validateModuleAuthorityEndpoint). An address
	// that does not resolve here is a different failure: the runtime, or the
	// service agent's rendered Deployment, injected no capability for this
	// endpoint. Binding no listener leaves it unreachable — connection refused,
	// never a weaker gate, and RunAuthority already treats a nil port that way.
	// Panicking instead would trade an auxiliary listener for the whole host:
	// accounts would crashloop and take the tenant surfaces, the internal tier
	// and every service that authenticates through them down with it.
	//
	// ResolveNetworkInstance never invents a port: the only outcomes are the
	// injected address or this error path.
	if authority, resolveErr := codefly.For(ctx).Endpoint(business.ModuleAuthorityEndpoint).API(standards.GRPC).ResolveNetworkInstance(); resolveErr != nil {
		wool.Get(ctx).In("main").Error(
			"the module authority endpoint did not resolve; no module authority listener is served",
			wool.Field("endpoint", business.ModuleAuthorityEndpoint),
			wool.ErrField(resolveErr),
		)
	} else {
		config.EndpointAuthorityPort = shared.Pointer(authority.Port)
	}

	// Complete dependency wiring, plugin registration and fixture seeding before
	// opening any listener. Starting the generated server first exposes handlers
	// while the package-level business service is still nil and makes a passing
	// port probe race with application readiness.
	if work != nil {
		clean, err := work(ctx)
		if err != nil {
			panic(err)
		}
		if clean != nil {
			defer clean()
		}
	}

	server, err := adapters.NewServer(config)
	if err != nil {
		panic(err)
	}

	go func() {
		if startErr := server.Start(context.Background()); startErr != nil {
			panic(startErr)
		}
	}()

	<-ctx.Done()
	server.Stop()
	fmt.Println("got interruption signal")

}
