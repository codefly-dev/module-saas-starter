package tools

import (
	"path/filepath"
	"regexp"
	"testing"
)

// The access token's revocation claims are read by two independent Go modules
// with no shared type between them: accounts mints them, the gateway parses them.
// A claim one side renames is a claim the other silently stops finding — and for
// a REVOCATION claim, silently stopping means admitting a token that was revoked.
//
// `fid` is the one this gate exists for. It carries the refresh family, which is
// stable across the rotation that changes `sid`, and a sign-out revokes it. If
// accounts stamps `fid` and the gateway looks for anything else, every access
// token minted before the last refresh survives the sign-out at the edge while
// accounts' own verifier refuses it — two components disagreeing about whether a
// person is signed out, with no test failing on either side.
//
// This gate proves the two sides name the same claims. It cannot prove either
// uses them correctly; that is what each side's behavioural tests are for.
func TestAccessClaimsMatchTheGateway(t *testing.T) {
	moduleDir := findModuleDir(t)
	minter := readFileOrFail(t, filepath.Join(moduleDir, "services", "accounts", "code",
		"pkg", "auth", "ed25519", "minter.go"))
	gateway := readFileOrFail(t, filepath.Join(moduleDir, "services", "auth-gateway", "code",
		"ext_authz.go"))

	for _, claim := range []struct{ field, tag string }{
		{"SessionID", "sid"},
		{"FamilyID", "fid"},
	} {
		t.Run(claim.tag, func(t *testing.T) {
			// The tag may carry options (`,omitempty`); the NAME is what travels.
			pattern := regexp.MustCompile(
				claim.field + `\s+string\s+` + "`" + `json:"` + claim.tag + `(,[a-z]+)?"` + "`")
			if !pattern.MatchString(minter) {
				t.Fatalf("accounts does not stamp %s as json:%q", claim.field, claim.tag)
			}
			if !pattern.MatchString(gateway) {
				t.Fatalf("the gateway does not parse %s as json:%q", claim.field, claim.tag)
			}
		})
	}

	// And both must actually CONSULT the family, not merely carry it. A claim
	// parsed and never checked is the same hole with an extra field in it.
	for name, source := range map[string]string{"accounts": minter, "gateway": gateway} {
		if !regexp.MustCompile(`claims\.SessionID, claims\.FamilyID`).MatchString(source) {
			t.Errorf("%s must check the family alongside the session when deciding revocation", name)
		}
	}
}
