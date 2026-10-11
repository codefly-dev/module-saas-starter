package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	"policy-log/mirror"
	policylogv1 "policy-log/pkg/gen/saas/policylog/v1"
	"policy-log/server"
	"policy-log/service"
	"policy-log/witness"

	"google.golang.org/grpc"
)

// TestWitnessHelperProcess serves the REAL witness stack — witness.Log,
// service.Service and this package's projection, over the in-memory object
// store with a provisioned genesis head and a fresh signing key — for a host's
// integration test to reach over the wire.
//
// It is a helper process, not a test: it runs only when a host test starts it
// with POLICY_LOG_WITNESS_HELPER set, serves until its stdin closes, and
// otherwise skips. The host's own test cannot link this module — accounts is a
// separate Go module, and it carries its own generated saas.policylog.v1 types,
// which the protobuf registry refuses to register twice at init — so the host
// runs in its own process and reaches this one exactly as it reaches a deployed
// witness: through the generated client, on a gRPC listener.
//
// This is test code and stays test code. The served binary never imports the
// memory store, never generates a key and never writes a genesis head; nothing
// here is a way to run the witness outside a test.
//
// The one line it prints, after the prefix below, is the listener address and
// the public key document a host pins — the same schema as Keys and as the
// trust-anchor document composition delivers.
func TestWitnessHelperProcess(t *testing.T) {
	if os.Getenv(witnessHelperEnv) == "" {
		t.Skip("helper process for a host integration test; started with " + witnessHelperEnv + " set")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log, err := witness.Open(ctx, genesisStore(t), private)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := mirror.Start(ctx, discard{}, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	witnessService, err := service.New(log, worker)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(witnessService, func(err error) { t.Logf("witness: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	policylogv1.RegisterWitnessServiceServer(grpcServer, handler)
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()

	if !ed25519.PublicKey(log.Keys().PublicKey).Equal(public) {
		t.Fatal("the witness publishes a key other than the one it was opened with")
	}
	announcement, err := json.Marshal(struct {
		Address string      `json:"address"`
		Key     witness.Key `json:"key"`
	}{Address: listener.Addr().String(), Key: log.Keys()})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(witnessHelperReady + string(announcement))

	// Serve until the host closes stdin: that is how it takes the witness
	// down mid-test, and how it stops the helper when it is done.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

const (
	witnessHelperEnv   = "POLICY_LOG_WITNESS_HELPER"
	witnessHelperReady = "POLICY_LOG_WITNESS_READY "
)
