// Package orgsettings is the generated-protobuf access layer for org-scoped
// generic settings — the organization analogue of pkg/usersettings. Product
// code never addresses JSON keys directly; it works through the composed field
// catalog. The Starter ships no common (hand-written) org settings today, so
// every field arrives through a settings contribution (scope: org) that the
// composition tool generates into the composed container.
package orgsettings

import (
	"fmt"
	"strings"

	saassettings "accounts/pkg/settings"
	"accounts/pkg/settingscatalog"

	"google.golang.org/protobuf/proto"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

const MaximumJSONBytes = 128 * 1024

var JSON = saassettings.MustJSONCodec(
	func() *gen.OrganizationSettings { return &gen.OrganizationSettings{} },
	MaximumJSONBytes,
)

// Resolve returns a clone. The Starter defines no common org settings, so there
// are no defaults to materialize; contributed composed fields resolve their own
// defaults through their product catalog. Stored documents stay sparse.
func Resolve(stored *gen.OrganizationSettings) (*gen.OrganizationSettings, error) {
	if stored == nil {
		return &gen.OrganizationSettings{}, nil
	}
	return proto.Clone(stored).(*gen.OrganizationSettings), nil
}

type resetField struct {
	clear func(*gen.OrganizationSettings) error
	has   func(*gen.OrganizationSettings) (bool, error)
}

// resetFieldForPath resolves a reset path to a typed clear/has pair. Only
// composed.<field> paths are supported: the Starter has no common org settings,
// so there is no fixed reset allowlist as usersettings has.
func resetFieldForPath(path string) (resetField, bool, error) {
	name, found := strings.CutPrefix(path, "composed.")
	if !found || name == "" || strings.Contains(name, ".") {
		return resetField{}, false, nil
	}
	for _, field := range settingscatalog.OrgFields() {
		if field.Name != name {
			continue
		}
		if err := saassettings.ValidateComposedField(&gen.OrganizationSettings{}, "composed", name); err != nil {
			return resetField{}, false, err
		}
		return resetField{
			clear: func(settings *gen.OrganizationSettings) error {
				_, err := saassettings.AccessComposedField(settings, "composed", name, true)
				return err
			},
			has: func(settings *gen.OrganizationSettings) (bool, error) {
				return saassettings.AccessComposedField(settings, "composed", name, false)
			},
		}, true, nil
	}
	return resetField{}, false, nil
}

// ValidateResetPaths rejects unknown paths, duplicates, and ambiguous requests
// that both patch and reset the same field.
func ValidateResetPaths(patch *gen.OrganizationSettings, paths []string) error {
	if patch == nil {
		patch = &gen.OrganizationSettings{}
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		field, ok, err := resetFieldForPath(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("org settings reset path %q is not supported", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("org settings reset path %q is duplicated", path)
		}
		seen[path] = struct{}{}
		present, err := field.has(patch)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("org settings path %q cannot be patched and reset together", path)
		}
	}
	return nil
}

// ApplyResets clears typed fields from an in-memory document. Persistence
// adapters apply the same validated paths directly to sparse ProtoJSON.
func ApplyResets(settings *gen.OrganizationSettings, paths []string) error {
	for _, path := range paths {
		field, ok, err := resetFieldForPath(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("org settings reset path %q is not supported", path)
		}
		if err := field.clear(settings); err != nil {
			return err
		}
	}
	return nil
}
