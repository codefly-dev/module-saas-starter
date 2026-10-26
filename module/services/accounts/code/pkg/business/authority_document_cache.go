package business

import (
	"sync"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// verifiedAuthorityDocuments keeps, per binding, the authority document its
// applied generation was verified to carry. Verifying is an inbox read and two
// signature checks; every Check naming a method and every solution-role read
// would otherwise repeat it. An entry answers only for the generation and
// digest it was verified at, so a pass that applies another generation is a
// miss and replaces it: the cache holds one entry per binding. The pinned
// keys are wired once at boot, so an entry never outlives the keys that
// verified it.
type verifiedAuthorityDocuments struct {
	mu        sync.Mutex
	byBinding map[string]verifiedAuthorityDocument
}

// verifiedAuthorityDocument is one generation's verified outcome. A nil doc is
// a generation delivered without an authority document. doc is shared by every
// reader and is never modified.
type verifiedAuthorityDocument struct {
	generation uint64
	digest     string
	doc        *compositionv1.AuthorityManifestV1
}

func (c *verifiedAuthorityDocuments) get(binding string, applied *SolutionHostBindingApplied) (verifiedAuthorityDocument, bool) {
	if applied == nil {
		return verifiedAuthorityDocument{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.byBinding[binding]
	if !ok || entry.generation != applied.Generation || entry.digest != applied.Digest {
		return verifiedAuthorityDocument{}, false
	}
	return entry, true
}

func (c *verifiedAuthorityDocuments) put(binding string, entry verifiedAuthorityDocument) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byBinding == nil {
		c.byBinding = map[string]verifiedAuthorityDocument{}
	}
	c.byBinding[binding] = entry
}
