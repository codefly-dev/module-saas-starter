//go:build pure

package business

import (
	"os"
	"testing"
)

// AN ENVELOPE A CELL IS ALREADY RUNNING MUST KEEP PARSING.
//
// The ceiling is re-validated on every read, deliberately: the rules live in
// this binary, so a revision an older binary accepted must stop granting if a
// newer one refuses it. That is right, and it has a cost nobody was paying —
// NARROWING WHAT PARSES IS A DEPLOYMENT-BREAKING CHANGE, and nothing said so.
//
// On a staging cell a rebuild produced an accounts that refused the stored
// envelope: "revision 12 no longer validates: module envelope is invalid".
// With no valid ceiling the registry derives nothing, so the gateway refuses
// every module exchange — "the module registry predates the committed module
// envelope" — and every module on the cell stops minting. Embedding, chat and
// ingest all fail at once, none of them naming the envelope. The only way back
// was to roll the image back.
//
// This fixture is the shape a cell is actually running: ten principals, their
// operation audiences, tenancy pinned, resources and queues as an
// administrator wrote them. Narrow the parser and this fails in CI rather than
// on a cell.
//
// If you are here because this failed: you have made a change that refuses an
// envelope already deployed. That is a migration, not an edit. Either accept
// the old shape and reject it at a later gate, or ship a rewrite of every
// stored envelope BEFORE the binary that requires it — and say which in the
// pull request.
func TestAnEnvelopeACellIsRunningStillParses(t *testing.T) {
	document, err := os.ReadFile("testdata/deployed-module-envelope.json")
	if err != nil {
		t.Fatalf("read the deployed envelope fixture: %v", err)
	}
	envelope, err := ParseModuleEnvelope(document)
	if err != nil {
		t.Fatalf("this build refuses an envelope a cell is running, which stops every mint on it: %v", err)
	}
	// Every principal the cell grants, by name. A build that parses the
	// document but silently drops a principal grants that module nothing,
	// which is the same outage arriving quietly.
	want := []string{
		"assistant", "calc", "drafts", "executor", "inference",
		"ledger", "library", "notes", "pages", "telemetry",
	}
	if len(envelope.Principals) != len(want) {
		t.Fatalf("parsed %d principals, want %d", len(envelope.Principals), len(want))
	}
	for _, principal := range want {
		if _, granted := envelope.Principals[principal]; !granted {
			t.Errorf("principal %q is in the deployed envelope and this build dropped it", principal)
		}
	}
	// A principal whose manifest publishes no contract legitimately carries
	// neither an operation audience nor a resource — the envelope stands whole
	// for it — so the count above is the assertion, not the contents.
}
