package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"accounts/fixtures"
	"accounts/pkg/auth/exchangeprobe"

	"golang.org/x/sys/unix"
)

// The accounts binary answers one diagnostic besides serving:
//
//	./app diagnose token-exchange
//
// It is for an operator, or a check, that can already exec into this container,
// and it grants that caller nothing it does not have: it reads the identity
// configuration this process reads, builds the provider stack this process
// builds, exchanges a sentinel code through that stack's own exchanger, and
// prints one JSON line — the verdicts, the HTTP status and the OAuth error code.
// Never the credential, never the provider's response body.
//
// The exchanger and everything beneath it log as they do when serving, and a
// provider's refusal is logged with its body, so while the diagnostic runs this
// process's stdout and stderr are the null device and only the result line
// reaches the caller.

const diagnoseCommand = "diagnose"

// diagnosticExchangeTimeout bounds the whole check: discovery, the exchange and
// its negative control.
const diagnosticExchangeTimeout = 30 * time.Second

// runRequestedDiagnostic runs the diagnostic args name and reports its exit
// code, or reports false when args request none and the process should serve.
func runRequestedDiagnostic(args []string) (int, bool) {
	if len(args) == 0 || args[0] != diagnoseCommand {
		return 0, false
	}
	if len(args) != 2 || args[1] != exchangeprobe.Check {
		fmt.Fprintf(os.Stderr, "usage: %s %s %s\n", os.Args[0], diagnoseCommand, exchangeprobe.Check)
		return 2, true
	}
	return runIsolatedDiagnostic(), true
}

// runIsolatedDiagnostic keeps a private handle on the caller's stdout for the
// result line, then points file descriptors 1 and 2 at the null device, so no
// log line, panic trace or library print from anything the check runs can reach
// the caller — whichever handle a logger captured. The process exits after it.
func runIsolatedDiagnostic() int {
	resultFD, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return 1
	}
	result := os.NewFile(uintptr(resultFD), "diagnostic-result")
	defer func() { _ = result.Close() }()
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return 1
	}
	defer func() { _ = discard.Close() }()
	for _, fd := range []int{1, 2} {
		if err := unix.Dup2(int(discard.Fd()), fd); err != nil {
			return 1
		}
	}
	if err := exchangeprobe.Write(result, tokenExchangeDiagnostic()); err != nil {
		return 1
	}
	return 0
}

// tokenExchangeDiagnostic certifies the configured provider's client
// authentication exactly as a login would exercise it.
func tokenExchangeDiagnostic() exchangeprobe.Result {
	selectedFixture, err := fixtures.SelectedName()
	if err != nil {
		return exchangeprobe.Result{Check: exchangeprobe.Check, Verdict: exchangeprobe.NotConfigured}
	}
	provider := configuredAuthProvider(selectedFixture)
	_, exchanger, err := buildProviderStack(provider, selectedFixture)
	if err != nil {
		// The reason is in this process's own startup refusal; it is not echoed
		// here, because a discovery failure can carry the provider's answer.
		return exchangeprobe.Result{Check: exchangeprobe.Check, Verdict: exchangeprobe.NotConfigured, Provider: provider}
	}
	redirectURI := ""
	if allowed := configuredOAuthRedirectURIs(); len(allowed) > 0 {
		redirectURI = allowed[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticExchangeTimeout)
	defer cancel()
	result := exchangeprobe.Run(ctx, exchanger, redirectURI)
	result.Provider = provider
	return result
}
