package executioncustody

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	base "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"google.golang.org/protobuf/proto"
)

// ClaimsDigest v1 hashes deterministic protobuf bytes of canonical decoded
// WorkContextV1, including nonce, key, scopes, lineage and expiry. Unknown fields
// are rejected. It is independent of JSON whitespace and bearer encoding.
func ClaimsDigest(claims *base.WorkContextV1) (string, error) {
	if claims == nil || len(claims.ProtoReflect().GetUnknown()) != 0 {
		return "", errors.New("known WorkContextV1 required")
	}
	for _, actor := range claims.GetActorChain() {
		if actor != nil && len(actor.ProtoReflect().GetUnknown()) != 0 {
			return "", errors.New("unknown actor fields")
		}
		for _, scope := range actor.GetGrantedScopes() {
			if scope != nil && len(scope.ProtoReflect().GetUnknown()) != 0 {
				return "", errors.New("unknown scope fields")
			}
		}
	}
	for _, scope := range claims.GetAuthorityScopes() {
		if scope != nil && len(scope.ProtoReflect().GetUnknown()) != 0 {
			return "", errors.New("unknown scope fields")
		}
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(claims)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "wc-claims-proto-v1:" + hex.EncodeToString(sum[:]), nil
}
