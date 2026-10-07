package infra

// The inventory being complete is not something a review can see. A purpose
// nobody re-seals keeps the outgoing key service referenced forever, while the
// sweep still reports success and the operator withdraws the backend on that
// report — turning every value under that purpose into a credential nothing can
// read.
//
// So the purpose set is DERIVED from pkg/business's own source rather than
// restated here. Enumerating it by hand, or by a grep whose output was read one
// page at a time, is how four of seven purposes once shipped covered.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// declaredPurposes reads pkg/business and returns every identifier that declares
// a secret purpose: a `const …Purpose = "…"` and a `func …Purpose(string) string`
// alike, exported or not. An unexported one is still a finding — it means a
// purpose exists that this package cannot name, which is itself the bug.
func declaredPurposes(t *testing.T) map[string]string {
	t.Helper()
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, "../business", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)

	found := map[string]string{}
	for _, pkg := range packages {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				switch declaration := node.(type) {
				case *ast.ValueSpec:
					for i, name := range declaration.Names {
						if !strings.HasSuffix(name.Name, "Purpose") || i >= len(declaration.Values) {
							continue
						}
						if literal, ok := declaration.Values[i].(*ast.BasicLit); ok && literal.Kind == token.STRING {
							found[name.Name] = path
						}
					}
				case *ast.FuncDecl:
					if declaration.Recv == nil && strings.HasSuffix(declaration.Name.Name, "Purpose") {
						found[declaration.Name.Name] = path
					}
				}
				return true
			})
		}
	}
	require.NotEmpty(t, found, "the parser found no purposes at all, so this gate is not checking anything")
	return found
}

// prefixConstants are the per-purpose prefixes each *Purpose function is built
// from. They are declarations ending in `Purpose` too, so the parser finds them;
// they are not purposes in their own right and are covered by the function that
// uses them.
var prefixConstants = map[string]string{
	"webhookSecretPurposePrefix":             "WebhookSecretPurpose",
	"datasourceConnectorSecretPurposePrefix": "DatasourceConnectorSecretPurpose",
	"datasourceWebhookSecretPurposePrefix":   "DatasourceWebhookSecretPurpose",
	"orgIdentityProviderSecretPurposePrefix": "OrgIdentityProviderSecretPurpose",
	"connectorSecretPurposePrefix":           "ConnectorSecretPurpose",
}

func TestResealCoversEveryDeclaredPurpose(t *testing.T) {
	// What the inventory actually re-seals, by the identifier each entry's
	// purpose function resolves to. Kept beside the inventory so adding a column
	// without claiming its purpose fails here.
	covered := map[string]string{
		"MFATOTPPurpose":                   "mfa_devices.secret_encrypted",
		"WebAuthnCredentialPurpose":        "webauthn_credentials.credential_encrypted",
		"WebAuthnSessionPurpose":           "webauthn_ceremonies.session_data_encrypted",
		"WebhookSecretPurpose":             "webhook_subscriptions.secret_encrypted",
		"DatasourceConnectorSecretPurpose": "datasource_sources.credential_secret_ref",
		"DatasourceWebhookSecretPurpose":   "datasource_sources.webhook_secret_ref",
		"OrgIdentityProviderSecretPurpose": "org_identity_providers.client_secret_ref",
		"ConnectorSecretPurpose":           "connector_credentials.secret_encrypted",
	}

	for purpose, where := range declaredPurposes(t) {
		if canonical, isPrefix := prefixConstants[purpose]; isPrefix {
			require.Contains(t, covered, canonical,
				"%s builds %s, which no enveloped column re-seals", purpose, canonical)
			continue
		}
		require.Contains(t, covered, purpose,
			"%s (declared in %s) is a secret purpose that ResealEnvelopes does not cover: "+
				"values sealed under it would keep the outgoing key service referenced after the "+
				"sweep reported success, and withdrawing that backend would make them unreadable. "+
				"Add the column that holds it to envelopedColumns", purpose, where)
	}

	// The inverse: an entry claiming a purpose that no longer exists would make
	// the gate above pass while re-sealing nothing.
	declared := declaredPurposes(t)
	for purpose := range covered {
		require.Contains(t, declared, purpose,
			"the inventory claims %s, which pkg/business no longer declares", purpose)
	}
}

// Every inventory entry must name a distinct column, or one of them is dead and
// the coverage it appears to give is imaginary.
func TestEnvelopedColumnsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, column := range envelopedColumns {
		key := column.table + "." + column.column
		require.False(t, seen[key], "%s appears twice in the inventory", key)
		seen[key] = true
		require.NotNil(t, column.purpose, "%s has no purpose", key)
	}
	require.Len(t, envelopedColumns, 9)
}
