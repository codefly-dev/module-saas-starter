package business

import "time"

// The identity an organisation installs (the adversarial review's §9).
//
// A solution's registry key is its route alias, and an alias is deliberately
// REUSABLE: a tombstoned one may be claimed by another binding. An installation
// that named the alias would therefore transfer consent without anybody acting —
// org A installs the binding behind `reports` and exposes it to a team, that
// binding is withdrawn, a different binding takes `reports`, and it inherits A's
// installation and A's team grant.
//
// A foreign key to the registry would make that inheritance formal rather than
// accidental: it guarantees referential integrity, not continuity of authorised
// identity. Core states the intended rule outright — a second instance "gets its
// own binding ID and inherits nothing from this one — no route, no generation
// history, no installation".
//
// So the thing an organisation installs has its own identity. A SolutionTarget is
// ONE CONTINUOUS PERIOD of one binding's presence: minted when a present
// generation applies for a binding with no live target, closed by that binding's
// tombstone generation, and never reused. A binding withdrawn and later delivered
// again gets a new target, because the tombstone ended the presence that was
// consented to and consenting again is a dynamic act by a person.
//
// The alias lives on the target and moves with a generation that renames it. That
// is exactly why it is not the identity.
type SolutionTarget struct {
	// ID is the immutable identity. An installation names this.
	ID string
	// BindingID is whose presence this target records. Not unique on its own: a
	// binding withdrawn and re-presented has one target per period.
	BindingID string
	// SolutionID is the route alias as of the applied generation, and may move.
	SolutionID string
	// OpenedGeneration is the generation that first presented this period.
	OpenedGeneration uint64
	// ClosedGeneration is the tombstone generation that withdrew it, or nil while
	// the target is live.
	ClosedGeneration *uint64
	OpenedAt         time.Time
	ClosedAt         *time.Time
}

// Live reports whether this target is the one an installation may currently name.
func (t *SolutionTarget) Live() bool { return t != nil && t.ClosedGeneration == nil }

// SolutionGenerationDecision is one entry in the generation history: a generation
// this host decided about, and what it decided (migration 19).
//
// It exists because nothing else can answer "what did this component move
// through". The binding row holds one desired and one applied generation, and its
// pending reason is the CURRENT refusal, overwritten every pass — so a superseded
// refusal leaves no trace at all.
type SolutionGenerationDecision struct {
	BindingID string
	// TargetID is the installable identity the decision applied into, empty for a
	// refusal and for a tombstone of a binding that never applied.
	TargetID   string
	Generation uint64
	Digest     string
	// Decision is applied | current | refused | tombstoned. `current` is recorded
	// deliberately: a re-read that changed nothing is still evidence the document
	// was present and unchanged, which is what separates "delivery stopped" from
	// "delivery kept saying the same thing".
	Decision string
	// Reason explains a refusal and is set for nothing else.
	Reason           string
	Release          string
	RegistryRevision *int64
	DecidedAt        time.Time
}

// Generation decisions, as recorded.
const (
	SolutionGenerationApplied    = "applied"
	SolutionGenerationCurrent    = "current"
	SolutionGenerationRefused    = "refused"
	SolutionGenerationTombstoned = "tombstoned"
)
