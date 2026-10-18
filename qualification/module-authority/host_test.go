package moduleauthority_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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

// host is the real saas-starter host this qualification runs the client
// library against: accounts' internal tier and `authority` endpoint in one
// helper process, and the auth-gateway's HTTP handler in another, connected to
// that accounts over the wire. Each half is a helper test compiled from its
// own service module — the only way to run either service's real handlers, since
// the gateway is a main package in a module that may not import accounts, and
// no process can link a service's generated saas.accounts.v1 types beside the
// client library's.
type host struct {
	// GatewayURL is the gateway's public endpoint, where a viewer's reads go;
	// WorkloadURL its workload endpoint, where a composed workload mints — the
	// address the composition points `platform/gateway-endpoint` at.
	GatewayURL    string
	WorkloadURL   string
	Authority     string
	InternalToken string
	Fixture       accountsReady
	// Flow is the gateway half's module read: the fixture person's confined
	// credential and the names it was declared under.
	Flow moduleReadFlow
}

// moduleReadFlow is the gateway half's announcement of the module read
// (moduleReadFlowReady in auth-gateway).
type moduleReadFlow struct {
	Bearer   string `json:"bearer"`
	Subject  string `json:"subject"`
	Org      string `json:"org"`
	Session  string `json:"session"`
	Solution string `json:"solution"`
	Module   string `json:"module"`
	Header   string `json:"header"`
}

// accountsReady is the accounts half's announcement
// (moduleAuthorityContractReadyLine in accounts/pkg/adapters).
type accountsReady struct {
	Internal  string `json:"internal"`
	Authority string `json:"authority"`
	Connect   string `json:"connect"`
	// ConsumerInstallation is the installation of the solution the confined
	// credential is delivered to.
	ConsumerInstallation string            `json:"consumer_installation"`
	Sources              map[string]string `json:"sources"`
	Modules              map[string]string `json:"modules"`
	Bindings             map[string]string `json:"bindings"`
	Tenant               string            `json:"tenant"`
	Owner                string            `json:"owner"`
	// Tokens are the projected ServiceAccount tokens of the accounts half's
	// fake pods, by module prefix, plus "unapproved": a pod of the source
	// module's workload running a build nothing approved.
	Tokens map[string]string `json:"tokens"`
}

const (
	accountsReadyPrefix = "MODULE_AUTHORITY_CONTRACT_READY "
	gatewayReadyPrefix  = "MODULE_AUTHORITY_CONTRACT_GATEWAY_READY "
	helperStartTimeout  = 60 * time.Second
)

var (
	hostOnce sync.Once
	hostErr  error
	shared   *host
	stops    []func()
)

// TestMain owns the helper processes: they are started once, shared by every
// test, and stopped whatever the outcome.
func TestMain(m *testing.M) {
	code := m.Run()
	for i := len(stops) - 1; i >= 0; i-- {
		stops[i]()
	}
	os.Exit(code)
}

func startHost(t *testing.T) *host {
	t.Helper()
	hostOnce.Do(func() { shared, hostErr = bootHost() })
	if hostErr != nil {
		t.Fatalf("start the host: %v", hostErr)
	}
	return shared
}

func bootHost() (*host, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, errors.New("cannot locate this file")
	}
	services := filepath.Join(filepath.Dir(file), "..", "..", "module", "services")
	bin, err := os.MkdirTemp("", "module-authority-host-")
	if err != nil {
		return nil, err
	}
	stops = append(stops, func() { _ = os.RemoveAll(bin) })

	accountsBin := filepath.Join(bin, "accounts.test")
	gatewayBin := filepath.Join(bin, "auth-gateway.test")
	if err := compileHelper(filepath.Join(services, "accounts", "code"), "./pkg/adapters", accountsBin); err != nil {
		return nil, err
	}
	if err := compileHelper(filepath.Join(services, "auth-gateway", "code"), ".", gatewayBin); err != nil {
		return nil, err
	}

	h := &host{InternalToken: randomSecret()}
	// One gateway credential and one public origin, given to both halves: the
	// trust and the address a deployment configures on each service alike.
	gatewayToken, publicBase := randomSecret(), "https://host.example.com"
	accountsInput, _ := json.Marshal(map[string]any{
		"internal_token": h.InternalToken, "gateway_token": gatewayToken, "public_base": publicBase,
	})
	line, err := startHelper(filepath.Join(services, "accounts", "code", "pkg", "adapters"), accountsBin,
		"TestModuleAuthorityContractHost", "MODULE_AUTHORITY_CONTRACT_HOST", string(accountsInput), accountsReadyPrefix)
	if err != nil {
		return nil, fmt.Errorf("accounts half: %w", err)
	}
	if err := json.Unmarshal([]byte(line), &h.Fixture); err != nil {
		return nil, fmt.Errorf("accounts half announced %q: %w", line, err)
	}
	h.Authority = h.Fixture.Authority

	gatewayInput, _ := json.Marshal(map[string]string{
		"internal_token": h.InternalToken, "accounts": h.Fixture.Internal,
		"connect": h.Fixture.Connect, "gateway_token": gatewayToken,
		"tenant": h.Fixture.Tenant, "owner": h.Fixture.Owner, "public_base": publicBase,
		"consumer_installation": h.Fixture.ConsumerInstallation,
	})
	line, err = startHelper(filepath.Join(services, "auth-gateway", "code"), gatewayBin,
		"TestModuleAuthorityContractGateway", "MODULE_AUTHORITY_CONTRACT_GATEWAY", string(gatewayInput), gatewayReadyPrefix)
	if err != nil {
		return nil, fmt.Errorf("gateway half: %w", err)
	}
	var gateway struct {
		Gateway  string         `json:"gateway"`
		Workload string         `json:"workload"`
		Flow     moduleReadFlow `json:"flow"`
	}
	if err := json.Unmarshal([]byte(line), &gateway); err != nil {
		return nil, fmt.Errorf("gateway half announced %q: %w", line, err)
	}
	if gateway.Workload == "" {
		return nil, fmt.Errorf("gateway half announced no workload endpoint: %q", line)
	}
	h.GatewayURL, h.WorkloadURL, h.Flow = gateway.Gateway, gateway.Workload, gateway.Flow
	return h, nil
}

// helperEnv is this process's environment for a child go command or helper,
// without GOFLAGS: a -modfile meant for this module must not leak into a
// service module's build.
func helperEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

func compileHelper(dir, pkg, out string) error {
	cmd := exec.Command("go", "test", "-c", "-o", out, pkg)
	cmd.Dir = dir
	cmd.Env = helperEnv("GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compile %s in %s: %w\n%s", pkg, dir, err, output)
	}
	return nil
}

// startHelper runs one helper test, waits for its announcement line and
// returns the JSON after prefix. The helper serves until its stdin closes.
func startHelper(dir, binary, test, envName, input, prefix string) (string, error) {
	cmd := exec.Command(binary, "-test.run=^"+test+"$", "-test.count=1")
	cmd.Dir = dir
	cmd.Env = helperEnv(envName + "=" + input)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// exited closes once the helper is gone; waitErr is set before it closes.
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	stops = append(stops, func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	lines := make(chan string, 1)
	var transcript lockedBuffer
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		announced := false
		for scanner.Scan() {
			text := scanner.Text()
			if !announced && strings.HasPrefix(text, prefix) {
				announced = true
				lines <- strings.TrimPrefix(text, prefix)
				continue
			}
			_, _ = transcript.Write([]byte(text + "\n"))
		}
	}()
	select {
	case line := <-lines:
		return line, nil
	case <-exited:
		return "", fmt.Errorf("%s exited before announcing (%v)\nstdout:\n%s\nstderr:\n%s", test, waitErr, transcript.String(), stderr.String())
	case <-time.After(helperStartTimeout):
		return "", fmt.Errorf("%s did not announce within %s\nstderr:\n%s", test, helperStartTimeout, stderr.String())
	}
}

func randomSecret() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*lockedBuffer)(nil)

// context for one call against the host.
func callContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
