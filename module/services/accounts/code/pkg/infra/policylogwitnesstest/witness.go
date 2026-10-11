// Package policylogwitnesstest starts the module's REAL policy-log witness for
// a test in this service: the witness's own server package, service and
// witness log over its in-memory object store, compiled from the policy-log
// module and run as a helper process (its TestWitnessHelperProcess).
//
// A helper process rather than an import, because neither half can link the
// other: policy-log is a separate Go module, and this service carries its own
// generated copy of the saas.policylog.v1 types, which the protobuf registry
// refuses to register twice. So the test reaches the witness the way a deployed
// host does — over gRPC, through the generated client — and checks what it
// signed against a key it pinned from the witness's own key document.
//
// Test support only: nothing outside a _test.go file imports this package.
package policylogwitnesstest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	helperEnv     = "POLICY_LOG_WITNESS_HELPER"
	helperReady   = "POLICY_LOG_WITNESS_READY "
	helperTest    = "TestWitnessHelperProcess"
	startDeadline = 60 * time.Second
)

// Witness is one running witness helper.
type Witness struct {
	// Address is its gRPC listener, host:port on loopback.
	Address string
	// KeyDocument is the public key document it published, in the schema of
	// the trust-anchor document a host pins.
	KeyDocument []byte

	stop sync.Once
	done chan struct{}
	in   io.WriteCloser
	cmd  *exec.Cmd
}

// Stop takes the witness down and waits for the process to exit. Safe to call
// more than once; also registered as the test's cleanup.
func (w *Witness) Stop() {
	w.stop.Do(func() {
		_ = w.in.Close()
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			_ = w.cmd.Process.Kill()
			<-w.done
		}
	})
}

var (
	compileOnce sync.Once
	binary      string
	compileErr  error
)

// Start compiles the helper once per test process and starts a fresh witness:
// a new signing key and an empty log at genesis.
func Start(t testing.TB) *Witness {
	t.Helper()
	dir, err := policyLogCode()
	if err != nil {
		t.Fatal(err)
	}
	compileOnce.Do(func() { binary, compileErr = compile(dir) })
	if compileErr != nil {
		t.Fatalf("compile the policy-log witness helper: %v", compileErr)
	}

	cmd := exec.Command(binary, "-test.run=^"+helperTest+"$", "-test.count=1", "-test.v")
	cmd.Dir = filepath.Join(dir, "server")
	cmd.Env = append(environment(), helperEnv+"=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &Witness{in: in, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(w.done) }()
	t.Cleanup(w.Stop)

	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		announced := false
		for scanner.Scan() {
			if text := scanner.Text(); !announced && strings.HasPrefix(text, helperReady) {
				announced = true
				lines <- strings.TrimPrefix(text, helperReady)
			}
		}
	}()
	select {
	case line := <-lines:
		var announcement struct {
			Address string          `json:"address"`
			Key     json.RawMessage `json:"key"`
		}
		if err := json.Unmarshal([]byte(line), &announcement); err != nil {
			t.Fatalf("the witness helper announced %q: %v", line, err)
		}
		w.Address, w.KeyDocument = announcement.Address, announcement.Key
		return w
	case <-w.done:
		t.Fatalf("the witness helper exited before announcing itself:\n%s", stderr.String())
	case <-time.After(startDeadline):
		t.Fatalf("the witness helper did not announce itself within %s:\n%s", startDeadline, stderr.String())
	}
	return nil
}

// policyLogCode is the policy-log module beside this service in the checkout.
func policyLogCode() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate this file")
	}
	// .../services/accounts/code/pkg/infra/policylogwitnesstest
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "policy-log", "code")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return "", fmt.Errorf("the policy-log module is not beside this service at %s: %w", dir, err)
	}
	return dir, nil
}

func compile(dir string) (string, error) {
	out, err := os.MkdirTemp("", "policy-log-witness-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(out, "witness.test")
	cmd := exec.Command("go", "test", "-c", "-o", bin, "./server")
	cmd.Dir = dir
	cmd.Env = environment()
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go test -c ./server in %s: %w\n%s", dir, err, output)
	}
	return bin, nil
}

// environment is this process's, without GOFLAGS: a flag meant for this module
// (a -modfile, a build tag) must not leak into the witness module's build.
func environment() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") && !strings.HasPrefix(kv, helperEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(env, "GOWORK=off")
}
