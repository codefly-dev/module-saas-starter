package moduleauthority_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// THE MODULE READ A CONFINED CREDENTIAL IS DELIVERED FOR, with the gateway's
// real Gateway.ServeHTTP in front of accounts' real StartTask, in one flow.
//
//  1. the client calls the solution's tool endpoint with a credential confined
//     to that solution;
//  2. the solution's runtime mints a Work Context for the module it consumes,
//     forwarding the same credential: the gateway routes it to accounts' Connect
//     tier, which authenticates the forwarded identity with the gateway
//     credential and answers it from the real WorkContextService/StartTask,
//     signed with core/workcontext under the key the JWKS publishes;
//  3. it reads the module with that capability and the same credential: the
//     gateway's edge inspects it with core's Inspector over accounts' JWKS and
//     pairs it with the credential's session, and the module receives exactly
//     what accounts signed.
//
// Each half is also pinned alone — the gateway with a double for accounts, and
// StartTask with a core Verifier — and this is the one place they meet.
func TestTheModuleReadThroughTheRealGatewayIsMintedByTheRealStartTask(t *testing.T) {
	h := startHost(t)
	flow := h.Flow
	if flow.Bearer == "" {
		t.Fatal("the gateway half did not serve the module read flow")
	}

	// 1. The tool call reaches the solution it was delivered for.
	tool := gatewayCall(t, h, http.MethodPost, "/solutions/"+flow.Solution+"/mcp", nil, nil)
	if tool.status != http.StatusOK {
		t.Fatalf("tool call: %d %s", tool.status, tool.body)
	}

	// 2. The mint, with the same credential, through the gateway to StartTask.
	// The request names no session: what is signed is the one the credential
	// authenticated, which is the one the gateway pairs on.
	mint := gatewayCall(t, h, http.MethodPost, "/saas.accounts.v1.WorkContextService/StartTask", map[string]any{
		"orgId":           flow.Org,
		"taskId":          "019f6bf7-1111-7111-8111-1111111111aa",
		"audience":        flow.Module,
		"ttlSeconds":      300,
		"authorityScopes": []map[string]any{{"resourceKind": flow.Module, "actions": []string{"read"}}},
	}, nil)
	if mint.status != http.StatusOK {
		t.Fatalf("StartTask through the gateway: %d %s", mint.status, mint.body)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mint.body, &issued); err != nil || issued.Token == "" {
		t.Fatalf("StartTask answered no capability: %s", mint.body)
	}

	// 3. The module read, with the capability and the same credential.
	read := gatewayCall(t, h, http.MethodGet, "/v1/"+flow.Module+"/collection", nil, map[string]string{flow.Header: issued.Token})
	if read.status != http.StatusOK {
		t.Fatalf("module read: %d %s", read.status, read.body)
	}
	var received map[string]string
	if err := json.Unmarshal(read.body, &received); err != nil {
		t.Fatalf("module answered %s: %v", read.body, err)
	}
	for field, want := range map[string]string{
		"issuer":   "saas-starter",
		"audience": flow.Module,
		"owner":    flow.Subject,
		"tenant":   flow.Org,
		"session":  flow.Session,
	} {
		if received[field] != want {
			t.Errorf("the module received %s %q, want %q", field, received[field], want)
		}
	}
	if received["installation"] == "" {
		t.Error("the capability is sealed to no installation")
	}

	// The ceiling is the consumer's delivered authority (#1083): a capability
	// addressed to the consumer itself is refused by name, and so is one asking
	// for more than the binding the consumer declares at the module.
	for name, ask := range map[string]struct {
		audience, kind string
		actions        []string
		refusal        string
	}{
		"addressed to the consumer itself": {flow.Solution, flow.Solution, []string{"read"}, "is the consumer this credential was delivered to"},
		"outside the declared binding":     {flow.Module, flow.Module, []string{"read", "write"}, "outside what the consumer this credential was delivered to declares"},
	} {
		refused := gatewayCall(t, h, http.MethodPost, "/saas.accounts.v1.WorkContextService/StartTask", map[string]any{
			"orgId":  flow.Org,
			"taskId": "019f6bf7-1111-7111-8111-1111111111ab", "audience": ask.audience, "ttlSeconds": 300,
			"authorityScopes": []map[string]any{{"resourceKind": ask.kind, "actions": ask.actions}},
		}, nil)
		if refused.status == http.StatusOK || !bytes.Contains(refused.body, []byte(ask.refusal)) {
			t.Errorf("a mint %s: %d %s, want the ceiling's refusal %q", name, refused.status, refused.body, ask.refusal)
		}
	}

	// The same capability without the credential it was minted under is
	// refused at the edge: the pairing is on the credential's session.
	bare := gatewayCallWith(t, h, http.MethodGet, "/v1/"+flow.Module+"/collection", nil, "", map[string]string{flow.Header: issued.Token})
	if bare.status == http.StatusOK {
		t.Fatalf("a capability presented without its credential reached the module: %s", bare.body)
	}
}

type answer struct {
	status int
	body   []byte
}

// gatewayCall sends one request to the gateway half with the flow's confined
// credential.
func gatewayCall(t *testing.T, h *host, method, path string, body any, headers map[string]string) answer {
	t.Helper()
	return gatewayCallWith(t, h, method, path, body, h.Flow.Bearer, headers)
}

func gatewayCallWith(t *testing.T, h *host, method, path string, body any, bearer string, headers map[string]string) answer {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(callContext(t), method, h.GatewayURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("authorization", "Bearer "+bearer)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{status: resp.StatusCode, body: read}
}
