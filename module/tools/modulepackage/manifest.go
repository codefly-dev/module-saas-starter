package modulepackage

import (
	"github.com/Masterminds/semver/v3"
	corecomposition "github.com/codefly-dev/core/composition"
)

const ManifestName = corecomposition.PackageManifestFileName

// Manifest is the authoritative Core package contract. Keeping this alias at
// the package boundary prevents the publisher and the consumer from drifting
// onto structurally different documents with the same schema identifier.
type Manifest = corecomposition.PackageManifest

type Command = corecomposition.PackageCommand

func ReadManifest(moduleRoot string) (Manifest, error) {
	manifest, err := corecomposition.LoadPackageManifest(moduleRoot)
	if err != nil {
		return Manifest{}, err
	}
	return *manifest, nil
}

func IsSemanticVersion(version string) bool {
	_, err := semver.StrictNewVersion(version)
	return err == nil
}
