package business

import (
	"maps"
	"slices"

	installationv1 "accounts/pkg/gen/host/installation/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// approvedRoles is what each role of inst's target reaches as inst's consent
// approved it (domain.ConsentReceipt.Reach): what its holders hold until an
// administrator installs a newer review. An update's review carries it beside
// the reach in the composition now, so a host can say what the update asks
// beyond it. None for a fresh install, an installation that is not active, or
// a consent recorded before reach was kept. The reach of every role held
// together (decide.JointRoles) is no role's, and is left out.
func approvedRoles(inst domain.Installation) ([]*installationv1.OfferRole, error) {
	if inst.ID == "" || inst.Status != domain.Active || inst.Consent.ReachDigest == "" {
		return nil, nil
	}
	approved, err := decide.DecodeReach(inst.Consent.Reach)
	if err != nil {
		return nil, err
	}
	var out []*installationv1.OfferRole
	for _, name := range slices.Sorted(maps.Keys(approved)) {
		if name == decide.JointRoles {
			continue
		}
		role := &installationv1.OfferRole{Name: name}
		for _, row := range approved[name] {
			reach := &installationv1.OfferReach{Path: slices.Clone(row.Path), Module: row.Module, Via: row.Via,
				Confinement: row.Confinement, Operations: slices.Clone(row.Operations)}
			for _, sc := range row.Scopes {
				reach.Scopes = append(reach.Scopes, &installationv1.OfferScope{ResourceKind: sc.GetResourceKind(), Actions: slices.Clone(sc.GetActions())})
			}
			role.Reach = append(role.Reach, reach)
		}
		out = append(out, role)
	}
	return out, nil
}
