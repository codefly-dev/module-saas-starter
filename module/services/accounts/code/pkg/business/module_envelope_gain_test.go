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
// could never be corrected.
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
	t.Run("an array that gains a member is a narrowing to be witnessed", func(t *testing.T) {
		// Deliberately: an array's members are not independent, and this cannot
		// tell an extension from a replacement.
		require.False(t, moduleEnvelopeOnlyAdds(envelope(headless),
			envelope(`{"audience":"model","headless_scopes":[{"actions":["read","write"],"resource_kind":"p"}]}`)))
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
