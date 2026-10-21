package frontendpayload

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// AssetVersion names the host's applied identity, never browser authority.
// A rollback has a new generation; a reopened binding has a new target.
func AssetVersion(binding, target string, generation uint64, release string) string {
	identity, _ := json.Marshal([]any{binding, target, generation, release})
	return fmt.Sprintf("%x", sha256.Sum256(identity))
}
