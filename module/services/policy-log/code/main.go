// policy-log is the independent signed witness for authority changes. This
// binary wires the delivered signing-key secret, the object store, the warehouse
// mirror and the transport-independent service onto the gRPC endpoint Codefly
// allocated, and refuses to start if the key or the store is not provisioned.
// The mirror is the one optional part: with neither of its settings delivered
// the witness runs without one and says so at boot (owner decision 2026-10-18).
//
// Nothing here may start a listener it cannot witness with, so every
// construction happens BEFORE net.Listen and any failure is fatal. A witness
// that accepted an append it could not sign, store or verify the history of
// would be worse than one that is down: the caller would commit against it.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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

var settingKeys = struct{ bucket, retentionDays, warehouseEndpoint, warehouseToken string }{
	bucket:            "POLICY_LOG_BUCKET",
	retentionDays:     "POLICY_LOG_RETENTION_DAYS",
	warehouseEndpoint: "POLICY_LOG_WAREHOUSE_ENDPOINT",
	warehouseToken:    "POLICY_LOG_WAREHOUSE_TOKEN",
}

type settings struct {
	bucket                            string
	retention                         time.Duration
	warehouseEndpoint, warehouseToken string
}

// mirrored reports whether a warehouse mirror was configured. resolveSettings
// guarantees the endpoint and token are delivered together or not at all.
func (s settings) mirrored() bool { return s.warehouseEndpoint != "" }

// resolveSettings reads the whole configuration from the delivered group and
// from nowhere else.
//
// There is deliberately no process-environment fallback, no default bucket and
// no development bypass. A witness that fell back to an ambient value would
// witness somewhere nobody provisioned — and it would do so by starting
// successfully, which is the one failure mode this service cannot have. Every
// missing key is named at once, so a half-provisioned group takes one restart
// to diagnose rather than three.
//
// The bucket is the witness's authority and is always required. The warehouse
// mirror is a best-effort projection of what the bucket already committed
// (README, "Warehouse mirror"), so it is optional — but only WHOLE: its endpoint
// and its token are delivered together or not at all. One without the other is
// a half-provisioned mirror, and it is refused rather than read as "no mirror",
// because that would silently drop a projection someone meant to configure.
func resolveSettings(value func(group, key string) (string, bool, error)) (settings, error) {
	// A value no namespace carries and an empty value are the same thing to this
	// service: nothing was provisioned. A value that is there and could not be
	// READ is not: it is refused by name, never read as absent — otherwise a
	// broken delivery of both mirror settings would boot claiming no mirror was
	// configured. No value is ever reported back, because one is a credential.
	var unreadable []string
	read := func(key string) string {
		got, set, err := value(policyLogConfiguration, key)
		if err != nil {
			unreadable = append(unreadable, key)
			return ""
		}
		if !set {
			return ""
		}
		return got
	}
	retentionDays := read(settingKeys.retentionDays)
	resolved := settings{
		bucket:            read(settingKeys.bucket),
		warehouseEndpoint: read(settingKeys.warehouseEndpoint),
		warehouseToken:    read(settingKeys.warehouseToken),
	}
	if len(unreadable) > 0 {
		return settings{}, fmt.Errorf("configuration group %q delivered %s but it could not be read: this service refuses rather than guess what was provisioned",
			policyLogConfiguration, strings.Join(unreadable, ", "))
	}
	var missing []string
	if resolved.bucket == "" {
		missing = append(missing, settingKeys.bucket)
	}
	// The entry retention is the bucket's other half: how long every entry is
	// Locked. Required, a whole number of days, and bounded — a Locked
	// retention can never be shortened, so a malformed value is refused rather
	// than rounded.
	if days, err := strconv.Atoi(retentionDays); err != nil || days <= 0 ||
		time.Duration(days)*24*time.Hour > gcs.MaxRetention || strconv.Itoa(days) != retentionDays {
		missing = append(missing, settingKeys.retentionDays)
	} else {
		resolved.retention = time.Duration(days) * 24 * time.Hour
	}
	switch {
	case resolved.warehouseEndpoint != "" && resolved.warehouseToken == "":
		missing = append(missing, settingKeys.warehouseToken)
	case resolved.warehouseEndpoint == "" && resolved.warehouseToken != "":
		missing = append(missing, settingKeys.warehouseEndpoint)
	}
	if len(missing) > 0 {
		return settings{}, fmt.Errorf("configuration group %q delivered no usable %s: composition must provision the bucket and its entry retention, and the warehouse mirror whole or not at all, before this service can witness",
			policyLogConfiguration, strings.Join(missing, ", "))
	}
	return resolved, nil
}

// noMirrorWarning is the boot line a witness with no warehouse mirror prints,
// so a reader of the pod log learns what the deployment chose rather than
// inferring it from a warehouse that stays empty.
const noMirrorWarning = "policy-log: no warehouse mirror is configured: this witness WITNESSES normally — " +
	"the object store is the authority and every receipt is signed, chained and committed there — " +
	"but committed entries are not projected to the warehouse until POLICY_LOG_WAREHOUSE_ENDPOINT and " +
	"POLICY_LOG_WAREHOUSE_TOKEN are delivered together; Read is the source to backfill it from"

// startMirror builds the warehouse projection the settings ask for: the
// bounded best-effort worker when one is configured, or mirror.None, stated at
// boot, when none is.
func startMirror(ctx context.Context, resolved settings, warn func(string)) (service.Mirror, error) {
	if !resolved.mirrored() {
		warn(noMirrorWarning)
		return mirror.None{}, nil
	}
	warehouse, err := mirror.NewWriter(resolved.warehouseEndpoint, resolved.warehouseToken)
	if err != nil {
		return nil, err
	}
	// A dropped or failed mirror is reported and repairable from Read. It never
	// changes an append's outcome, so it is logged, not fatal.
	return mirror.Start(ctx, warehouse, func(err error) {
		log.Printf("policy-log: warehouse mirror: %v", err)
	})
}

// resolveSigningKey reads only the declared secret group. The public pin is
// independently delivered to clients; this service cannot choose or replace it.
func resolveSigningKey(secret func(string, string) (string, error)) (ed25519.PrivateKey, error) {
	value, err := secret(policyLogConfiguration, identity.SigningKeySetting)
	if err != nil {
		return nil, fmt.Errorf("policy-log: secret %s/%s is required", policyLogConfiguration, identity.SigningKeySetting)
	}
	key, err := identity.ParseSigningKey(value)
	if err != nil {
		return nil, fmt.Errorf("policy-log: secret %s/%s: %w", policyLogConfiguration, identity.SigningKeySetting, err)
	}
	return key, nil
}

func run(ctx context.Context, stop func()) error {
	resolved, err := resolveSettings(codefly.For(ctx).WorkspaceValueIfSet)
	if err != nil {
		return err
	}
	grpcNetwork, err := codefly.For(ctx).Endpoint(grpcEndpointName).API("grpc").ResolveNetworkInstance()
	if err != nil {
		return fmt.Errorf("resolve Codefly endpoint %q: %w", grpcEndpointName, err)
	}
	if grpcNetwork == nil || grpcNetwork.Port == 0 {
		return fmt.Errorf("Codefly did not inject a port for the %q endpoint", grpcEndpointName)
	}
	port := grpcNetwork.Port
	key, err := resolveSigningKey(codefly.For(ctx).WorkspaceSecret)
	if err != nil {
		return err
	}
	store, err := gcs.New(ctx, resolved.bucket, resolved.retention)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	worker, err := startMirror(ctx, resolved, func(line string) { log.Print(line) })
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
