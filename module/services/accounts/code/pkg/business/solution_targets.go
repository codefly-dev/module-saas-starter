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

// RevokedInstallation is one installation a withdrawal ended: enough to attribute
// the audit event, and nothing more. The caller emits per organisation because an
// installation-revoked event belongs to the tenant whose consent ended, and a
// withdrawal can end several at once.
type RevokedInstallation struct {
	InstallationID string
	OrgID          string
}

// AvailableSolutionQuery narrows the catalogue read. It exists so the listing and
// the single-target check an install performs are ONE query with one acceptance
// predicate: a second query that meant to agree with this one is how an install
// ends up accepting something the catalogue would not offer.
type AvailableSolutionQuery struct {
	// OrgID is whose installations decide the `installed` flag. Empty answers
	// false for every row, which is what a check that does not care wants.
	OrgID string
	// TargetID narrows to one target. When set, Cursor is ignored.
	TargetID string
	Cursor   string
	Limit    int
}

// AvailableSolutionTarget is one row of the catalogue: a solution target an
// administrator may install right now.
//
// It is built from ACCEPTED applied state and deliberately not from the
// diagnostic binding listing, which also reports desired generations that were
// refused. A refused generation is something an operator must see and something
// an administrator must not be able to consent to: consenting to a release this
// host never admitted records authority over a presence that does not exist.
type AvailableSolutionTarget struct {
	TargetID   string
	BindingID  string
	RouteAlias string
	// The applied release, split so a consumer renders it without parsing.
	ReleasePublisher string
	ReleaseName      string
	ReleaseVersion   string
	OpenedGeneration uint64
	// AppliedGeneration is the binding's newest applied generation, which is >=
	// OpenedGeneration once a later present generation has been applied.
	AppliedGeneration uint64
	// Installed reports whether the asking organisation already holds an ACTIVE
	// installation of this target.
	Installed bool
}

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
