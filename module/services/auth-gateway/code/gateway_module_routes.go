package main

// The /v1/<alias> namespace and a registry record's declared kind.
//
// A composed module is not proxied from here: every module call, a browser's
// included, goes through the hop (gateway_hop.go), which the module verifies.
// What remains is the reading of a /v1/ path's first segment, which keeps a
// module's routes from shadowing this host's own catalog, and the declared kind
// of a registry record, which the solution and frontend surfaces read.

import (
	"strings"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
)

// v1Prefix extracts the `<prefix>` from a `/v1/<prefix>/...` (or `/v1/<prefix>`)
// path. It reports false for any path not under /v1/ or with an empty prefix.
//
// A custom-verb route ends its first segment with `:<verb>`
// (`/v1/permissions:check`, `/v1/work-contexts:renew`), and the verb is not part
// of the prefix. Splitting only on `/` made `permissions:check` the "prefix" of
// that route, which left the real prefix `permissions` absent from
// ReservedV1Prefixes — and made the lookup below compare a verb-suffixed segment
// against declared aliases, so `/v1/<module>:<verb>` could never reach a module
// that owns the prefix.
func v1Prefix(path string) (string, bool) {
	const root = "/v1/"
	if !strings.HasPrefix(path, root) {
		return "", false
	}
	rest := path[len(root):]
	prefix := rest
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		prefix = rest[:idx]
	}
	if idx := strings.IndexByte(prefix, ':'); idx >= 0 {
		prefix = prefix[:idx]
	}
	if prefix == "" {
		return "", false
	}
	return prefix, true
}

// declaredModuleKind reports whether a record's declaration says "module".
//
// The kind is read from the declaration, never from the record's shape. A
// publisher prefix or an alias convention would be a guess at the one question
// that separates a surface with per-viewer admission from one without, and
// UNSPECIFIED — what a reader decodes from a writer that did not set the field —
// is not a kind: it answers false here and false in the solution check, so such a
// record is served by neither surface rather than by whichever needs less
// authority.
func declaredModuleKind(record *accountsv1.SolutionRegistration) bool {
	return record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE
}

// declaredSolutionKind is the same read for the solution surface.
func declaredSolutionKind(record *accountsv1.SolutionRegistration) bool {
	return record.GetDeclared().GetKind() == accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_SOLUTION
}
