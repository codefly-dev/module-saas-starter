package moduleauthority_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/codefly-dev/saas-sdk-go/gen/saas/accounts/v1"
	"github.com/codefly-dev/saas-sdk-go/gen/saas/accounts/v1/accountsv1connect"
	"github.com/codefly-dev/saas-sdk-go/moduleauthority"
)

// The published module-authority client, against the real host: the
// auth-gateway's module broker for every mint, accounts' `authority` endpoint
// for the exchange. Nothing here is a fake of either seam — the one thing a
// fake could never prove is that the client and the host agree on the wire.

type gatewaySeam struct{ url string }

func (g gatewaySeam) BaseURL() string          { return g.url }
func (g gatewaySeam) HTTPClient() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

func newClient(t *testing.T, h *host, prefix, secret string) *moduleauthority.Client {
	t.Helper()
	client, err := moduleauthority.New(moduleauthority.Seams{
		Gateway:       gatewaySeam{url: h.GatewayURL},
		Authority:     moduleauthority.Authority{Address: h.Authority},
		InternalToken: h.InternalToken,
	}, moduleauthority.Credentials{Prefix: prefix, Secret: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func sourceClient(t *testing.T, h *host) *moduleauthority.Client {
	prefix := h.Fixture.Modules["source"]
	return newClient(t, h, prefix, h.Secrets[prefix])
}

func runtimeClient(t *testing.T, h *host) *moduleauthority.Client {
	prefix := h.Fixture.Modules["runtime"]
	return newClient(t, h, prefix, h.Secrets[prefix])
}

func TestModuleWorkContextMintsAtTheGatewayBroker(t *testing.T) {
	h := startHost(t)
	token, err := sourceClient(t, h).ModuleWorkContext(callContext(t))
	if err != nil {
		t.Fatalf("ModuleWorkContext: %v", err)
	}
	if token.Encoded() == "" {
		t.Fatal("the broker answered an empty module Work Context")
	}

	wrong := newClient(t, h, h.Fixture.Modules["source"], "not-the-identity-secret")
	if _, err := wrong.ModuleWorkContext(callContext(t)); !errors.Is(err, moduleauthority.ErrInvalidCredentials) {
		t.Fatalf("a wrong identity secret: got %v, want ErrInvalidCredentials", err)
	}
}

func TestMintModuleOperationContextForAHeadlessBinding(t *testing.T) {
	h := startHost(t)
	client := sourceClient(t, h)
	binding := h.Fixture.Bindings["headless"]

	op, err := client.MintModuleOperationContext(callContext(t), binding)
	if err != nil {
		t.Fatalf("MintModuleOperationContext(%s): %v", binding, err)
	}
	if op.Token.Encoded() == "" || op.BindingID != binding || op.Audience != "indexservice" || op.PrincipalID == "" || op.Tenant == "" {
		t.Fatalf("unexpected operation context: %+v", op)
	}
	if !op.ExpiresAt.After(time.Now()) {
		t.Fatalf("operation context already expired: %v", op.ExpiresAt)
	}

	// A binding that declares no headless scopes is a proven module refused.
	if _, err := client.MintModuleOperationContext(callContext(t), h.Fixture.Bindings["delegation"]); !errors.Is(err, moduleauthority.ErrPermissionDenied) {
		t.Fatalf("a binding without headless scopes: got %v, want ErrPermissionDenied", err)
	}
}

func TestMintSourceOperationContextThroughAnActiveDelegation(t *testing.T) {
	h := startHost(t)
	source := h.Fixture.Sources["active"]

	minted, err := sourceClient(t, h).MintSourceOperationContext(callContext(t), source)
	if err != nil {
		t.Fatalf("MintSourceOperationContext(active): %v", err)
	}
	if minted.SourceID != source || minted.Tenant != h.Fixture.Tenant || minted.OwnerPrincipalID != h.Fixture.Owner ||
		minted.Audience != h.Fixture.Modules["runtime"] || minted.BindingID != h.Fixture.Bindings["delegation"] ||
		minted.DelegationID == "" || minted.Token.Encoded() == "" {
		t.Fatalf("unexpected source operation context: %+v", minted)
	}
}

// Each refusal reaches the client as its typed error: 412 DELEGATION_MISSING,
// and 403 carrying DELEGATION_REVOKED or DELEGATION_INVALID.
func TestMintSourceOperationContextRefusalsAreTyped(t *testing.T) {
	h := startHost(t)
	client := sourceClient(t, h)
	for name, want := range map[string]error{
		"missing": moduleauthority.ErrDelegationMissing,
		"revoked": moduleauthority.ErrDelegationRevoked,
		"invalid": moduleauthority.ErrDelegationInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.MintSourceOperationContext(callContext(t), h.Fixture.Sources[name])
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			var broker *moduleauthority.BrokerError
			if !errors.As(err, &broker) {
				t.Fatalf("%v carries no broker answer", err)
			}
			wantStatus, wantReason := http.StatusForbidden, "DELEGATION_"+strings.ToUpper(name)
			if name == "missing" {
				wantStatus = http.StatusPreconditionFailed
			}
			if broker.StatusCode != wantStatus || broker.Reason != wantReason {
				t.Fatalf("broker answered %d %q, want %d %q", broker.StatusCode, broker.Reason, wantStatus, wantReason)
			}
		})
	}
}

// The runtime module exchanges the delegation-bearing parent the source module
// minted for it, on accounts' authority endpoint, never at the gateway.
func TestExchangeOperationOfADelegationBearingParent(t *testing.T) {
	h := startHost(t)
	parent, err := sourceClient(t, h).MintSourceOperationContext(callContext(t), h.Fixture.Sources["active"])
	if err != nil {
		t.Fatalf("mint the parent: %v", err)
	}

	exchanged, err := runtimeClient(t, h).ExchangeOperation(callContext(t), moduleauthority.ExchangeRequest{
		BindingID: h.Fixture.Bindings["exchange"],
		Parent:    parent.Token,
	})
	if err != nil {
		t.Fatalf("ExchangeOperation: %v", err)
	}
	if exchanged.Encoded() == "" || exchanged.Encoded() == parent.Token.Encoded() {
		t.Fatal("the exchange answered no new Work Context")
	}

	// The parent is addressed to the runtime module: the source module cannot
	// exchange it.
	_, err = sourceClient(t, h).ExchangeOperation(callContext(t), moduleauthority.ExchangeRequest{
		BindingID: h.Fixture.Bindings["exchange"],
		Parent:    parent.Token,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an exchange by a module the parent is not addressed to: got %v, want PermissionDenied", err)
	}
}

// The gateway never serves the internal ModuleCapabilitiesService procedures
// at its edge: a Connect call there is a 404, which is why the client mints
// through the broker and exchanges on the authority endpoint.
func TestTheGatewayDoesNotServeTheCapabilitySurface(t *testing.T) {
	h := startHost(t)
	for _, procedure := range []string{
		accountsv1connect.ModuleCapabilitiesServiceMintModuleWorkContextProcedure,
		accountsv1connect.ModuleCapabilitiesServiceMintSourceOperationContextProcedure,
		accountsv1connect.ModuleCapabilitiesServiceExchangeDelegatedOperationAudienceProcedure,
	} {
		t.Run(procedure, func(t *testing.T) {
			req, err := http.NewRequestWithContext(callContext(t), http.MethodPost, h.GatewayURL+procedure, bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Connect-Protocol-Version", "1")
			req.Header.Set("X-Codefly-Internal-Token", h.InternalToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("POST %s at the gateway: %d %q, want 404", procedure, resp.StatusCode, body)
			}
		})
	}

	// And the generated Connect client, pointed at the gateway, gets nothing.
	direct := accountsv1connect.NewModuleCapabilitiesServiceClient(http.DefaultClient, h.GatewayURL)
	_, err := direct.MintModuleWorkContext(callContext(t), connect.NewRequest(&v1.ModuleMintWorkContextRequest{
		Prefix: h.Fixture.Modules["source"], Secret: h.Secrets[h.Fixture.Modules["source"]],
	}))
	if err == nil {
		t.Fatal("the gateway minted a module Work Context over Connect")
	}
}
