package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every way a module obtains a capability, held to the same behaviour through
// the REAL BindExecution (review round 1 on #1056, finding 4). A wiring gate
// can say a call is there; only this says a refusal withholds the capability.
//
// Each entrypoint mints for a pod of the principal's own workload running its
// approved build, and mints NOTHING — no token in the answer — for a token the
// API server refuses, a token from another workload, a pod on an unapproved
// build, an API server that cannot answer, and an audit record that cannot be
// committed.

// failingAudit refuses every record, so a capability whose issuance cannot be
// recorded must be withheld.
type failingAudit struct{}

func (failingAudit) Emit(context.Context, business.AuditEntry) {}
func (failingAudit) EmitTx(context.Context, business.AuditEntry) error {
	return errors.New("audit spine unavailable")
}

type mintEntrypoint struct {
	name string
	// install wires a fresh service and issuer, and returns the prefix that
	// mints and the mint itself, answering the capability it was given (empty
	// when none was).
	install func(t *testing.T) (prefix string, mint func(token string) (string, error))
}

const installerPrincipals = `{"example-installer":{"workload":{"service_account":"installer","namespace":"acme-prod","container":"app"},"tenant":"11111111-1111-4111-8111-111111111111"}}`

func mintEntrypoints() []mintEntrypoint {
	return []mintEntrypoint{
		{"MintModuleWorkContext", func(t *testing.T) (string, func(string) (string, error)) {
			installModuleWorkContextService(t, documentsPrincipals)
			installModuleWorkContextAuthority(t)
			return "documents", func(token string) (string, error) {
				resp, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(),
					&gen.ModuleMintWorkContextRequest{Prefix: "documents", ServiceAccountToken: token})
				return resp.GetToken(), err
			}
		}},
		{"MintModuleOperationContext", func(t *testing.T) (string, func(string) (string, error)) {
			installModuleWorkContextService(t, headlessPrincipals)
			installModuleWorkContextAuthority(t)
			return "documents", func(token string) (string, error) {
				resp, err := mintModuleOperationContext("documents", token, "model")
				return resp.GetToken(), err
			}
		}},
		{"MintSourceOperationContext", func(t *testing.T) (string, func(string) (string, error)) {
			installSourceDelegationService(t)
			return "docstore", func(token string) (string, error) {
				resp, err := mintSourceContext("docstore", token, business.SourceDelegationRef{DelegationID: delegationID})
				return resp.GetToken(), err
			}
		}},
		{"installer token route", func(t *testing.T) (string, func(string) (string, error)) {
			installModuleWorkContextService(t, installerPrincipals)
			installModuleWorkContextAuthority(t)
			policy, _, _ := installerPolicyFixture()
			raw, err := json.Marshal(policy)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "policy.json")
			require.NoError(t, os.WriteFile(path, raw, 0o600))
			handler, err := NewModuleInstallationHTTPHandler(service, path)
			require.NoError(t, err)
			return "example-installer", func(token string) (string, error) {
				req := httptest.NewRequest(http.MethodPost, moduleInstallationPrefix+"token",
					bytes.NewBufferString(`{"prefix":"example-installer"}`))
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				var answer struct {
					Token string `json:"token"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &answer)
				if w.Code != http.StatusOK {
					return answer.Token, fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
				}
				return answer.Token, nil
			}
		}},
	}
}

func TestEveryModuleMintIsBoundToTheRunningBuild(t *testing.T) {
	refusals := map[string]func(t *testing.T, prefix string) string{
		"a token the API server refuses": func(*testing.T, string) string { return "not-a-projected-token" },
		"a pod of another workload": func(_ *testing.T, prefix string) string {
			declared := moduleWorkload(moduleExecution.registry[business.ModulePrincipalID(prefix)])
			declared.ServiceAccount = "intruder"
			return moduleExecution.cluster.Run(declared, executiontest.Image(prefix))
		},
		"a pod on an unapproved build": func(_ *testing.T, prefix string) string {
			return moduleExecution.cluster.Run(moduleWorkload(moduleExecution.registry[business.ModulePrincipalID(prefix)]),
				executiontest.Image(prefix+"-unreviewed"))
		},
		"a pod with a debug container beside the app": func(t *testing.T, prefix string) string {
			token := moduleToken(t, prefix)
			moduleExecution.cluster.AddContainer(token, business.ContainerExecution{
				Name: "debug", Kind: business.ContainerEphemeral, State: business.ContainerRunning,
				ImageID: executiontest.Image("debugger"),
			})
			return token
		},
		"the API server cannot answer": func(t *testing.T, prefix string) string {
			token := moduleToken(t, prefix)
			moduleExecution.cluster.Unreachable = true
			return token
		},
		"the mint cannot be recorded": func(t *testing.T, prefix string) string {
			service.SetAuditEmitter(failingAudit{})
			return moduleToken(t, prefix)
		},
	}
	for _, entry := range mintEntrypoints() {
		t.Run(entry.name, func(t *testing.T) {
			t.Run("its own pod on its approved build mints", func(t *testing.T) {
				prefix, mint := entry.install(t)
				capability, err := mint(moduleToken(t, prefix))
				require.NoError(t, err)
				require.NotEmpty(t, capability)
			})
			for name, token := range refusals {
				t.Run(name, func(t *testing.T) {
					prefix, mint := entry.install(t)
					capability, err := mint(token(t, prefix))
					require.Error(t, err)
					require.Empty(t, capability, "a refused mint must hand out no capability")
				})
			}
		})
	}
}

// A revision can commit after identity binding, while the issuance is recorded.
// Every real entrypoint must withhold the already signed token at its final fence.
type mintEnvelopeStore struct {
	latest uint64
}

func (s *mintEnvelopeStore) LatestModuleEnvelopeRevision(context.Context) (uint64, error) {
	return s.latest, nil
}
func (*mintEnvelopeStore) LatestModuleEnvelopeRecord(context.Context) (*business.ModuleEnvelopeRecord, error) {
	panic("not used")
}
func (*mintEnvelopeStore) AppendModuleEnvelopeRecord(context.Context, *business.ModuleEnvelopeRecord, uint64) error {
	panic("not used")
}

type mintAuditCallback struct{ duringRecord func() }

func (mintAuditCallback) Emit(context.Context, business.AuditEntry) {}
func (a mintAuditCallback) EmitTx(context.Context, business.AuditEntry) error {
	a.duringRecord()
	return nil
}

func TestEveryModuleMintFencesAuthorityAfterSigning(t *testing.T) {
	for _, entry := range mintEntrypoints() {
		t.Run(entry.name, func(t *testing.T) {
			for _, change := range []string{"unchanged", "committed revision", "registry replacement"} {
				t.Run(change, func(t *testing.T) {
					prefix, mint := entry.install(t)
					envelope := &mintEnvelopeStore{}
					service.SetModuleEnvelopeStore(envelope)
					service.SetAuditEmitter(mintAuditCallback{duringRecord: func() {
						switch change {
						case "committed revision":
							envelope.latest++
						case "registry replacement":
							service.SetModulePrincipals(nil)
						}
					}})
					capability, err := mint(moduleToken(t, prefix))
					if change == "unchanged" {
						require.NoError(t, err)
						require.NotEmpty(t, capability)
					} else {
						require.Error(t, err)
						require.Empty(t, capability, "the signed token must be withheld")
						if entry.name == "installer token route" {
							require.ErrorContains(t, err, "HTTP 503")
						} else {
							require.Equal(t, codes.Unavailable, status.Code(err))
						}
					}
				})
			}
		})
	}
}
