package business

// A delivery composes a module twice over: accounts' own delivery records it
// (the signed carrier, its applied generation, the registry every route and
// workload review reads), and the authority core records the module it
// decides with. Both read the same two signed documents; the core reads them
// through its shared delivered package, never a reading of its own here.

import (
	"context"
	"fmt"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	coredelivered "github.com/codefly-dev/authority/libraries/authority-core/go/delivered"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"google.golang.org/protobuf/proto"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// coreModuleOf is what the core composes from a delivery's manifest and the
// authority document paired with it, if any.
func coreModuleOf(manifest *solutionhost.ModuleManifest, carrierDocument []byte, signed *compositionv1.SignedManifestV1) (domain.Module, error) {
	var doc *coredelivered.Document
	if signed != nil {
		var wire compositionv1.AuthorityManifestV1
		if err := proto.Unmarshal(signed.GetDocument(), &wire); err != nil {
			return domain.Module{}, domain.Malformed("the authority's document does not read: %v", err)
		}
		doc = documentOf(&wire)
		if err := coredelivered.Pair(*doc, carrierDocument, manifest); err != nil {
			return domain.Module{}, err
		}
	}
	return coredelivered.Module(manifest, doc)
}

// documentOf reads this host's binding of the authority's document as the core's.
func documentOf(in *compositionv1.AuthorityManifestV1) *coredelivered.Document {
	doc := &coredelivered.Document{Module: in.GetModule(), Generation: in.GetGeneration(), CoreManifestDigest: in.GetCoreManifestDigest(),
		ScopeCeilings: wireScopes(in.GetScopeCeilings()), OwnerAware: in.GetOwnerAware()}
	for _, m := range in.GetMethods() {
		doc.Methods = append(doc.Methods, coredelivered.Method{Method: m.GetMethod(), Requires: wireScopes(m.GetRequires()), AnyCheckedCaller: m.GetAnyCheckedCaller()})
	}
	for _, r := range in.GetRoles() {
		doc.Roles = append(doc.Roles, coredelivered.Role{Name: r.GetName(), Scopes: wireScopes(r.GetScopes()), InstallationOwned: wireScopes(r.GetInstallationOwnedScopes())})
	}
	return doc
}

// wireScopes converts the interface's scopes to core's, byte for byte.
func wireScopes(in []*authorityv1.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		raw, _ := proto.Marshal(sc)
		c := &basev0.WorkScopeV1{}
		_ = proto.Unmarshal(raw, c)
		out = append(out, c)
	}
	return out
}

// precheckCoreModule refuses, before anything is recorded, a delivery the core
// would refuse to compose. It reads the carrier unverified, so it only ever
// refuses: what is applied is read again from the verified, applied record.
func precheckCoreModule(req *compositionv1.ApplyManifestRequest) error {
	manifest, err := solutionhost.ManifestFromVerified(req.GetCarrier().GetDocument())
	if err != nil {
		return nil // the delivery path names a carrier that does not read
	}
	if manifest.Removed {
		return nil
	}
	m, err := coreModuleOf(manifest, req.GetCarrier().GetDocument(), req.GetAuthority())
	if err != nil {
		return err
	}
	m.ResourceKinds = decide.Served(m)
	return decide.ValidateModule(m)
}

// applyCoreModule records the applied, verified delivery in the core: the
// module it composes at its generation, or its withdrawal. A replay of the
// generation the core already holds changes nothing.
func (s *Service) applyCoreModule(ctx context.Context, manifest *solutionhost.ModuleManifest, carrierDocument []byte, signed *compositionv1.SignedManifestV1) ([]string, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, err
	}
	if manifest.Removed {
		if _, err := core.Composed(ctx, manifest.Alias); err != nil {
			if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
				return nil, nil
			}
			return nil, err
		}
		return nil, core.WithdrawModule(ctx, manifest.Alias)
	}
	m, err := coreModuleOf(manifest, carrierDocument, signed)
	if err != nil {
		return nil, err
	}
	if held, err := core.Composed(ctx, m.Name); err == nil && held.Generation == m.Generation && !held.StandIn {
		return nil, nil
	}
	return core.ApplyModule(ctx, m)
}

// coreDeliveryError is a refusal of the core's, carried through the delivery
// path's own error vocabulary.
func coreDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	code, ok := domain.CodeOf(err)
	switch {
	case !ok:
		return fmt.Errorf("%w: the authority core: %w", ErrSolutionDeliveryHostState, err)
	case code == domain.Invalid:
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	case code == domain.Precondition:
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryConflict, err)
	}
	return fmt.Errorf("%w: %w", ErrSolutionDeliveryUnattested, err)
}

