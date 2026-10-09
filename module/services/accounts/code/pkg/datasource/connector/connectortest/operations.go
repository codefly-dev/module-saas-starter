package connectortest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"accounts/pkg/datasource/operations"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OperationsFixture drives the host call path with a scripted provider. It is
// separate from FilesFixture: operations admission makes no sync claim.
type OperationsFixture interface {
	Declare(*testing.T, []operations.Declaration)
	Invoke(context.Context, string, string, json.RawMessage) error
	ProviderReply(status int, body string, loseReply bool)
	ProviderCalls() int
	Credential() string
}

func OperationDeclaration() operations.Declaration {
	return operations.Declaration{Name: "read_item", Method: "GET", Path: "/items/{id}", Effect: operations.ReadOnly, MaxOutputBytes: 128,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string"},"body":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

// AssertNoCredentialSegments is shared by transport and durable-sink tests.
// It checks every overlapping segment, rather than just one sentinel prefix.
func AssertNoCredentialSegments(t *testing.T, secrets []string, sinks ...string) {
	t.Helper()
	for _, secret := range secrets {
		for i := 0; i+16 <= len(secret); i++ {
			for _, sink := range sinks {
				if strings.Contains(sink, secret[i:i+16]) {
					t.Fatal("credential segment was disclosed")
				}
			}
		}
	}
}

func RunOperations(t *testing.T, newFixture func(*testing.T) OperationsFixture) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*testing.T, OperationsFixture)
	}{
		{"unknown operation", func(t *testing.T, f OperationsFixture) {
			expectOperationCode(t, f.Invoke(t.Context(), "unknown", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{}`)), codes.NotFound)
			if f.ProviderCalls() != 0 {
				t.Fatal("unknown operation reached provider")
			}
		}},
		{"schema refusal", func(t *testing.T, f OperationsFixture) {
			err := f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a","undeclared":"value"}`))
			expectOperationCode(t, err, codes.InvalidArgument)
			if f.ProviderCalls() != 0 {
				t.Fatal("invalid input reached provider")
			}
			AssertNoCredentialSegments(t, []string{f.Credential()}, err.Error())
		}},
		{"path parameter required", func(t *testing.T, f OperationsFixture) {
			expectOperationCode(t, f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{}`)), codes.InvalidArgument)
			if f.ProviderCalls() != 0 {
				t.Fatal("missing path reached provider")
			}
		}},
		{"GET body refused", func(t *testing.T, f OperationsFixture) {
			expectOperationCode(t, f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a","body":"value"}`)), codes.InvalidArgument)
			if f.ProviderCalls() != 0 {
				t.Fatal("GET body reached provider")
			}
		}},
		{"output cap", func(t *testing.T, f OperationsFixture) {
			f.ProviderReply(200, `{"value":"`+string(make([]byte, 129))+`"}`, false)
			expectOperationCode(t, f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a"}`)), codes.FailedPrecondition)
		}},
		{"typed rate limit", func(t *testing.T, f OperationsFixture) {
			f.ProviderReply(429, f.Credential(), false)
			err := f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a"}`))
			expectOperationCode(t, err, codes.ResourceExhausted)
			reason, retry := false, false
			for _, d := range status.Convert(err).Details() {
				switch d := d.(type) {
				case *errdetails.ErrorInfo:
					reason = d.Reason == "DATASOURCE_RATE_LIMITED" && d.Metadata["reset_at"] != ""
				case *errdetails.RetryInfo:
					retry = d.RetryDelay.AsDuration() > 0
				}
			}
			if !reason || !retry {
				t.Fatal("rate limit lacks ErrorInfo/RetryInfo")
			}
			AssertNoCredentialSegments(t, []string{f.Credential()}, err.Error())
		}},
		{"lost mutation reply cannot redispatch", func(t *testing.T, f OperationsFixture) {
			declaration := OperationDeclaration()
			declaration.Method = "POST"
			declaration.Effect = operations.Mutation
			f.Declare(t, []operations.Declaration{declaration})
			f.ProviderReply(200, f.Credential(), true)
			for i := 0; i < 2; i++ {
				err := f.Invoke(t.Context(), "read_item", "11111111-1111-4111-8111-111111111111", json.RawMessage(`{"id":"a"}`))
				expectOperationCode(t, err, codes.FailedPrecondition)
				AssertNoCredentialSegments(t, []string{f.Credential()}, err.Error())
			}
			if f.ProviderCalls() != 1 {
				t.Fatal("uncertain mutation was redispatched")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.Declare(t, []operations.Declaration{OperationDeclaration()})
			tc.run(t, f)
		})
	}
}
func expectOperationCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}
