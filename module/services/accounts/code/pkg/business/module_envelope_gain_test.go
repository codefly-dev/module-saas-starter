package business

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// WHAT MAY BE WRITTEN WITHOUT A WITNESS.
//
// The policy log witnesses NARROWINGS, because a narrowing nothing witnessed is
// one a restore undoes silently. A write that only grants is the opposite: a
// restore that undid it leaves LESS authority, which is the fail-closed
// direction, so it is applied and audited without one.
//
// That distinction is load-bearing here and not theoretical. No deployment has
// a policy log transport — the warehouse cannot issue the receipt the protocol
// requires — so a write classified as a narrowing cannot be made at all. While
// a principal had to be byte-identical to count as unchanged, the only writable
// change was a whole new principal, and a module missing one arm of its ceiling
// could never be corrected; while an array had to be identical, a module that
// declared one more content kind could not be granted it.
func TestAPrincipalMayGainWithoutAWitness(t *testing.T) {
	envelope := func(documentsModel string) []byte {
		return []byte(`{"revision":1,"principals":{"documents":{"grant":{"tenant":"t",` +
			`"operation_audiences":{"model":` + documentsModel + `}}}}}`)
	}
	headless := `{"audience":"model","headless_scopes":[{"actions":["read"],"resource_kind":"p"}]}`
	bothArms := `{"audience":"model","headless_scopes":[{"actions":["read"],"resource_kind":"p"}],` +
		`"invoke_scopes":[{"actions":["read"],"resource_kind":"p"}]}`

	t.Run("an arm added to a binding it already had", func(t *testing.T) {
		require.True(t, moduleEnvelopeOnlyAdds(envelope(headless), envelope(bothArms)),
			"the binding gains an arm and no installation loses anything")
	})
	t.Run("the same document", func(t *testing.T) {
		require.True(t, moduleEnvelopeOnlyAdds(envelope(headless), envelope(headless)))
	})
	t.Run("a whole principal added", func(t *testing.T) {
		before := []byte(`{"revision":1,"principals":{"a":{"grant":{}}}}`)
		after := []byte(`{"revision":2,"principals":{"a":{"grant":{}},"b":{"grant":{}}}}`)
		require.True(t, moduleEnvelopeOnlyAdds(before, after))
	})

	t.Run("an arm REMOVED is a narrowing", func(t *testing.T) {
		require.False(t, moduleEnvelopeOnlyAdds(envelope(bothArms), envelope(headless)))
	})
	t.Run("a principal removed is a narrowing", func(t *testing.T) {
		before := []byte(`{"revision":1,"principals":{"a":{"grant":{}},"b":{"grant":{}}}}`)
		after := []byte(`{"revision":2,"principals":{"a":{"grant":{}}}}`)
		require.False(t, moduleEnvelopeOnlyAdds(before, after))
	})
	t.Run("a value CHANGED is a narrowing, even to something longer", func(t *testing.T) {
		require.False(t, moduleEnvelopeOnlyAdds(envelope(headless),
			envelope(`{"audience":"other","headless_scopes":[{"actions":["read"],"resource_kind":"p"}]}`)))
	})
	t.Run("an action added to a binding's scope is a gain", func(t *testing.T) {
		require.True(t, moduleEnvelopeOnlyAdds(envelope(headless),
			envelope(`{"audience":"model","headless_scopes":[{"actions":["read","write"],"resource_kind":"p"}]}`)))
		require.False(t, moduleEnvelopeOnlyAdds(
			envelope(`{"audience":"model","headless_scopes":[{"actions":["read","write"],"resource_kind":"p"}]}`),
			envelope(headless)), "and the action removed is a narrowing")
	})
	t.Run("resource ids appearing on a scope that had none is a narrowing", func(t *testing.T) {
		bounded := `{"audience":"model","headless_scopes":[{"actions":["read"],"resource_kind":"p","resource_ids":["one"]}]}`
		require.False(t, moduleEnvelopeOnlyAdds(envelope(headless), envelope(bounded)),
			"no resource ids is every resource")
		require.True(t, moduleEnvelopeOnlyAdds(envelope(bounded), envelope(headless)))
		require.True(t, moduleEnvelopeOnlyAdds(envelope(bounded),
			envelope(`{"audience":"model","headless_scopes":[{"actions":["read"],"resource_kind":"p","resource_ids":["one","two"]}]}`)))
	})
	t.Run("an allowlist that gains a member is a gain", func(t *testing.T) {
		// documents' resources gaining its collection and ingest kinds: what a
		// composition's envelope moves by when a module declares more content.
		before := []byte(`{"revision":1,"principals":{"documents":{"grant":{"tenant":"t","resources":["documents"],"namespaces":["document"]}}}}`)
		after := []byte(`{"revision":2,"principals":{"documents":{"grant":{"tenant":"t","resources":["documents","documents.collection","documents.ingest"],"namespaces":["document"],"queues":["ingest"]}}}}`)
		require.True(t, moduleEnvelopeOnlyAdds(before, after))
		require.False(t, moduleEnvelopeOnlyAdds(after, before), "and a kind removed is a narrowing")
	})
	t.Run("a cross-tenant grant withdrawn is a narrowing", func(t *testing.T) {
		shared := []byte(`{"revision":1,"principals":{"a":{"grant":{"tenant":"t","cross_tenant":true}}}}`)
		pinned := []byte(`{"revision":2,"principals":{"a":{"grant":{"tenant":"t"}}}}`)
		require.False(t, moduleEnvelopeOnlyAdds(shared, pinned))
		require.True(t, moduleEnvelopeOnlyAdds(pinned, shared))
	})
	t.Run("an artifact approval changed is a narrowing", func(t *testing.T) {
		policy := func(digest string) []byte {
			return []byte(`{"revision":1,"principals":{"a":{"grant":{"artifact_policies":{"p":{"schema":"s","contracts":[{"kind":"k","name":"n","digest":"` + digest + `"}]}}}}}}`)
		}
		require.False(t, moduleEnvelopeOnlyAdds(policy("one"), policy("two")))
		require.True(t, moduleEnvelopeOnlyAdds(policy("one"), policy("one")))
	})
	t.Run("a tenant retargeted is a narrowing", func(t *testing.T) {
		before := []byte(`{"revision":1,"principals":{"a":{"grant":{"tenant":"one"}}}}`)
		after := []byte(`{"revision":2,"principals":{"a":{"grant":{"tenant":"two"}}}}`)
		require.False(t, moduleEnvelopeOnlyAdds(before, after),
			"an installation in the old tenant loses everything")
	})
	t.Run("a top-level key that is not principals must be unchanged", func(t *testing.T) {
		before := []byte(`{"revision":1,"principals":{},"other":1}`)
		require.False(t, moduleEnvelopeOnlyAdds(before, []byte(`{"revision":2,"principals":{},"other":2}`)))
		require.False(t, moduleEnvelopeOnlyAdds(before, []byte(`{"revision":2,"principals":{}}`)))
	})
	t.Run("a document that does not decode is never an addition", func(t *testing.T) {
		require.False(t, moduleEnvelopeOnlyAdds([]byte(`{`), []byte(`{"revision":1,"principals":{}}`)))
		require.False(t, moduleEnvelopeOnlyAdds([]byte(`{"revision":1,"principals":{}}`), []byte(`nope`)))
	})
	t.Run("a principal that stops being an object is not a gain", func(t *testing.T) {
		before := []byte(`{"revision":1,"principals":{"a":{"grant":{"tenant":"t"}}}}`)
		after := []byte(`{"revision":2,"principals":{"a":"t"}}`)
		require.False(t, moduleEnvelopeOnlyAdds(before, after))
	})

	t.Run("the real correction this exists for", func(t *testing.T) {
		// documents' model binding, missing the invoke arm its manifest asks
		// for, which is what left every embedding refused on a cell.
		var before, after json.RawMessage = envelope(headless), envelope(bothArms)
		require.True(t, moduleEnvelopeOnlyAdds(before, after))
		require.False(t, moduleEnvelopeOnlyAdds(after, before), "and undoing it is a narrowing")
	})
}

func TestExistingBindingDelegationModeChangeRequiresAWitness(t *testing.T) {
	envelope := func(mode string) []byte {
		return []byte(`{"revision":1,"principals":{"reader":{"grant":{"operation_audiences":{"evidence":{"audience":"documents","invoke_scopes":[{"resource_kind":"documents","actions":["read"]}]` + mode + `}}}}}}`)
	}
	ordinary := envelope("")
	explicitOrdinary := envelope(`,"delegation_mode":""`)
	content := envelope(`,"delegation_mode":"content_read"`)
	require.False(t, moduleEnvelopeOnlyAdds(ordinary, content), "adding restrictive semantics is not adding authority")
	require.False(t, moduleEnvelopeOnlyAdds(content, ordinary))
	require.False(t, moduleEnvelopeOnlyAdds(explicitOrdinary, content))
	require.True(t, moduleEnvelopeOnlyAdds(content, content))
	require.True(t, moduleEnvelopeOnlyAdds([]byte(`{"revision":1,"principals":{"reader":{"grant":{}}}}`), content), "a wholly new binding grants authority")
}
