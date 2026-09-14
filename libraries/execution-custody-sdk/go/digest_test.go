package executioncustody

import (
	"testing"

	base "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestClaimsDigestV1CanonicalVector(t *testing.T) {
	const expected = "wc-claims-proto-v1:099cbc0b6ef3b1584d0f34fd61448593710c5d6a4a8baea831df9b5ec07e797b"
	for _, evidence := range []string{`{"taskId":"task","sessionId":"session"}`, `{ "session_id": "session", "task_id": "task" }`} {
		var claims base.WorkContextV1
		if err := protojson.Unmarshal([]byte(evidence), &claims); err != nil {
			t.Fatal(err)
		}
		actual, err := ClaimsDigest(&claims)
		if err != nil || actual != expected {
			t.Fatal(actual, err)
		}
	}
}

func TestClaimsDigestRejectsUnknownFieldsAtEveryNestedPosition(t *testing.T) {
	for _, position := range []string{"root", "actor", "owner-scope", "actor-scope"} {
		t.Run(position, func(t *testing.T) {
			claims := &base.WorkContextV1{AuthorityScopes: []*base.WorkScopeV1{{ResourceKind: "example"}}, ActorChain: []*base.WorkActorV1{{PrincipalId: "actor", GrantedScopes: []*base.WorkScopeV1{{ResourceKind: "example"}}}}}
			if _, err := ClaimsDigest(claims); err != nil {
				t.Fatal(err)
			}
			var message protoreflect.Message
			switch position {
			case "root":
				message = claims.ProtoReflect()
			case "actor":
				message = claims.ActorChain[0].ProtoReflect()
			case "owner-scope":
				message = claims.AuthorityScopes[0].ProtoReflect()
			case "actor-scope":
				message = claims.ActorChain[0].GrantedScopes[0].ProtoReflect()
			}
			message.SetUnknown([]byte{0xa0, 0x06, 0x01})
			if _, err := ClaimsDigest(claims); err == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
	if _, err := ClaimsDigest(nil); err == nil {
		t.Fatal("nil claims accepted")
	}
}
