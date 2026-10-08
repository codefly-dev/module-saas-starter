// policy-log is the independent signed witness for authority changes. This
// binary wires the fixed-path signing key, the object store, the warehouse
// mirror and the transport-independent service onto the gRPC endpoint Codefly
// allocated, and refuses to start if any one of them is not provisioned.
//
// Nothing here may start a listener it cannot witness with, so every
// construction happens BEFORE net.Listen and any failure is fatal. A witness
// that accepted an append it could not sign, store or verify the history of
// would be worse than one that is down: the caller would commit against it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"policy-log/identity"
	"policy-log/mirror"
	"policy-log/objectstore/gcs"
	policylogv1 "policy-log/pkg/gen/saas/policylog/v1"
	"policy-log/server"
	"policy-log/service"
	"policy-log/witness"

	codefly "github.com/codefly-dev/sdk-go"
	"google.golang.org/grpc"
)

// grpcEndpointName is the endpoint this service declares in
// service.codefly.yaml. It is the endpoint's NAME, which is how the port is
// looked up; the port itself is allocated by Codefly and never written here.
const grpcEndpointName = "grpc"

// policyLogConfiguration is the workspace configuration group composition
// provisions for this service. It is the ONLY source of these settings.
const policyLogConfiguration = "policy-log"

var settingKeys = struct{ bucket, warehouseEndpoint, warehouseToken string }{
	bucket:            "POLICY_LOG_BUCKET",
	warehouseEndpoint: "POLICY_LOG_WAREHOUSE_ENDPOINT",
	warehouseToken:    "POLICY_LOG_WAREHOUSE_TOKEN",
}

type settings struct{ bucket, warehouseEndpoint, warehouseToken string }

// resolveSettings reads the whole configuration from the delivered group and
// from nowhere else.
//
// There is deliberately no process-environment fallback, no default bucket and
// no development bypass. A witness that fell back to an ambient value would
// witness somewhere nobody provisioned — and it would do so by starting
// successfully, which is the one failure mode this service cannot have. Every
// missing key is named at once, so a half-provisioned group takes one restart
// to diagnose rather than three.
func resolveSettings(value func(group, key string) (string, error)) (settings, error) {
	var resolved settings
	var missing []string
	for _, field := range []struct {
		key  string
		into *string
	}{
		{settingKeys.bucket, &resolved.bucket},
		{settingKeys.warehouseEndpoint, &resolved.warehouseEndpoint},
		{settingKeys.warehouseToken, &resolved.warehouseToken},
	} {
		// A group that is absent and a key that is empty are the same thing to
		// this service: nothing was provisioned. Neither value is reported back,
		// because one of the three is a credential.
		got, err := value(policyLogConfiguration, field.key)
		if err != nil || got == "" {
			missing = append(missing, field.key)
			continue
		}
		*field.into = got
	}
	if len(missing) > 0 {
		return settings{}, fmt.Errorf("configuration group %q delivered no %s: composition must provision the whole group before this service can witness",
			policyLogConfiguration, strings.Join(missing, ", "))
	}
	return resolved, nil
}

func run(ctx context.Context, stop func()) error {
	resolved, err := resolveSettings(func(group, key string) (string, error) {
		return codefly.For(ctx).WorkspaceValue(group, key)
	})
	if err != nil {
		return err
	}
	endpoint, err := codefly.For(ctx).API(grpcEndpointName).ResolveNetworkInstance()
	if err != nil {
		return fmt.Errorf("resolve Codefly endpoint %q: %w", grpcEndpointName, err)
	}
	if endpoint == nil || endpoint.Port == 0 {
		return fmt.Errorf("Codefly did not resolve a port for the %q endpoint", grpcEndpointName)
	}
	port := endpoint.Port
	// The fixed path takes no argument and consults no override: a key this
	// process chose would witness under an identity nobody pinned.
	key, err := identity.LoadSigningKey()
	if err != nil {
		return err
	}
	store, err := gcs.New(ctx, resolved.bucket)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	warehouse, err := mirror.NewWriter(resolved.warehouseEndpoint, resolved.warehouseToken)
	if err != nil {
		return err
	}
	// A dropped or failed mirror is reported and repairable from Read. It never
	// changes an append's outcome, so it is logged, not fatal.
	worker, err := mirror.Start(ctx, warehouse, func(err error) {
		log.Printf("policy-log: warehouse mirror: %v", err)
	})
	if err != nil {
		return err
	}
	// Open verifies the entire committed chain under this key. It is deliberately
	// ahead of net.Listen: a witness that cannot verify its own history, or whose
	// head is absent or regressed, must never reach a state where it can accept
	// an append.
	witnessLog, err := witness.Open(ctx, store, key)
	if err != nil {
		return err
	}
	appendOnly, err := service.New(witnessLog, worker)
	if err != nil {
		return err
	}
	handler, err := server.New(appendOnly, func(err error) {
		log.Printf("policy-log: witness: %v", err)
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	// No reflection and no health service: this listener is private to the
	// module and its surface is the four witness operations, nothing more.
	grpcServer := grpc.NewServer()
	policylogv1.RegisterWitnessServiceServer(grpcServer, handler)
	go func() {
		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Printf("policy-log: serve: %v", err)
			stop()
		}
	}()
	fmt.Printf("policy log witness listening on Codefly gRPC endpoint %d\n", port)
	<-ctx.Done()
	grpcServer.GracefulStop()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	provider, err := codefly.Init(ctx)
	if err != nil {
		log.Fatalf("policy-log: %v", err)
	}
	ctx = provider.Inject(ctx)
	defer codefly.CatchPanic(ctx)
	if err := run(ctx, stop); err != nil {
		log.Fatalf("policy-log: %v", err)
	}
}
