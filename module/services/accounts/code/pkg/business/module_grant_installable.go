package business

import (
	"encoding/json"
	"errors"
	"fmt"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// ErrModuleGrantNotInstallable reports a module principal whose grant no
// organisation could consent to: written out as the installation scope that
// consents to all of it, it is larger than the installations contract lets an
// installation scope carry.
var ErrModuleGrantNotInstallable = errors.New("module principal grant is larger than an installation scope may carry")

// installationScopeMaxBytes is the most one installation scope document may
// carry, read from the installations contract (InstallSolutionRequest.scope)
// rather than restated, so the bound a grant is held to and the bound a
// person's consent is held to are one number.
var installationScopeMaxBytes = contractStringMaxBytes(&gen.InstallSolutionRequest{}, "scope")

// contractStringMaxBytes is the max_bytes rule a request message's contract
// states for one of its string fields. A field that states none is a contract
// change this host must not absorb silently, so it panics at start.
func contractStringMaxBytes(message proto.Message, field string) int {
	descriptor := message.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(field))
	if descriptor == nil {
		panic(fmt.Sprintf("%s has no field %q", message.ProtoReflect().Descriptor().FullName(), field))
	}
	rules, _ := proto.GetExtension(descriptor.Options(), validate.E_Field).(*validate.FieldRules)
	limit := rules.GetString().GetMaxBytes()
	if limit == 0 {
		panic(fmt.Sprintf("%s states no max_bytes", descriptor.FullName()))
	}
	return int(limit) //nolint:gosec // a contract byte bound, far below MaxInt
}

// validateInstallableGrant holds one principal's grant to the size its
// consent can be written in.
//
// THE BOUND IS THE CONSENT DOCUMENT, NOT A COUNT. Every operation binding a
// principal holds is installed into an organisation as part of its
// installation scope (FullInstallationScope), which the host persists and which
// a person narrows by writing the scope out; the installations contract bounds
// that document. A grant whose own full scope does not fit could never be
// consented to as written, so it is refused here, by name, where delivery
// records it — not at an organisation's install, long after the module shipped.
//
// It replaces a count of 32 operation bindings that stated no reason and
// predated derived bindings: a composition derives one binding per endpoint
// operation a module installs (one capability per operation, so each call
// carries only that operation's scopes), so the count grows with every module
// a solution composes and a fixed count turns that least-privilege shape into
// an outage. The byte bound still caps every per-binding cost that grows with
// the set — the registry's memory, the revision hash a headless context is
// checked against, the scope a host persists per installation.
func validateInstallableGrant(prefix string, grant ModulePrincipalGrant) error {
	document, err := json.Marshal(FullInstallationScope(grant))
	if err != nil {
		return fmt.Errorf("module principal %q: its installation scope does not encode: %w", prefix, err)
	}
	if len(document) > installationScopeMaxBytes {
		return fmt.Errorf(
			"%w: module principal %q holds %d operation bindings and its full installation scope encodes to %d bytes, more than the %d bytes an installation scope may carry, so no organisation could consent to it",
			ErrModuleGrantNotInstallable, prefix, len(grant.OperationAudiences), len(document), installationScopeMaxBytes)
	}
	return nil
}
