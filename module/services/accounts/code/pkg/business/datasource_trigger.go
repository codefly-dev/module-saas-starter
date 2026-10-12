package business

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A source's reconcile schedule is DECLARED here and FIRED somewhere else.
//
// Every connected source has a reconcile interval: the safety net for lost
// webhooks, and the only path to a new version of a source whose provider has
// no webhook receiver at all. This host used to fire it — a sweep on a
// one-minute tick that read the due rows and enqueued a job for each. It no
// longer does. The schedule is a TRIGGER a declared release carries, and the
// module that executes declared work owns the clock, the occurrences and
// the cursor.
//
// What stays here is what this host owns:
//
//   - the interval, as the source's own configuration. Editing it re-declares
//     the trigger, because the declaration is derived from it and nothing else.
//   - the declaration itself: the cron the interval spells, the policies, the
//     deadline, the input, and the identity occurrences run under.
//   - the identity. An occurrence has no person present, so there is nothing to
//     sign a Work Context from; a release names whose authority it borrows and
//     this host re-derives that authority from current facts on every single
//     occurrence (source_delegation.go).
//   - the operation an occurrence admits (ReconcileSource), which is the same
//     work a person asks for by hand.
//
// What does NOT stay here is a clock. There is no sweep, no due query, no lease
// and no tick: a host that still fired this would be two schedules for one
// source, and the one nobody declared would win.

const (
	// DatasourceReconcileRelease is the kind-of-work name this host's reconcile
	// declaration installs under. It is a name, not a reference: the immutable
	// reference a composition pins is derived from the installed manifest and
	// the worker that hosts it, neither of which is this host's to know.
	DatasourceReconcileRelease = "saas_datasource_reconcile"

	// DatasourceReconcileOperation is the host operation one occurrence admits,
	// by its method path — the same path a person's reconcile takes.
	DatasourceReconcileOperation = "/saas.accounts.v1.DatasourceService/ReconcileSource"

	// DatasourceTriggerTimezone is the location every declared reconcile cron is
	// read in. UTC, and deliberately not the organization's: a safety net that
	// moved twice a year would skip or double an occurrence on each transition,
	// and nothing about reconciling a source is local to anybody.
	DatasourceTriggerTimezone = "UTC"

	// DatasourceTriggerMissedRun and DatasourceTriggerOverlap are the policies
	// this host declares, in the executing module's vocabulary.
	//
	// SKIP for both, and each for its own reason. A reconcile is a snapshot of
	// the source as it is NOW: catching up four missed occurrences would take
	// four identical snapshots of one present state, so only the most recent
	// missed occurrence is worth admitting. And a pull provider re-sends its
	// whole content on every sync, so a second occurrence beside a running one
	// is a second full re-send of a source that is already being read.
	DatasourceTriggerMissedRun = "MISSED_RUN_POLICY_SKIP"
	DatasourceTriggerOverlap   = "OVERLAP_POLICY_SKIP"

	// DatasourceTriggerDeadline bounds one occurrence's run. It is the pull
	// sync's own job ceiling: an occurrence hands the work to the source's sync
	// engine and waits for nothing, so the bound is on handing it over.
	DatasourceTriggerDeadline = 10 * time.Minute
)

// The reasons a source declares no trigger. They are the two honest answers,
// and neither is an error: a source may have no schedule, and an interval the
// cron grammar cannot express must be said rather than rounded.
const (
	// TriggerUndeclarableNoSchedule: the source's reconcile interval is zero.
	// A source with no schedule is one that syncs at connect and then only when
	// a person asks.
	TriggerUndeclarableNoSchedule = "the source has no reconcile schedule"
	// TriggerUndeclarableInterval: the interval is not one a five-field cron
	// expression can name. See datasourceTriggerCron for what it can.
	TriggerUndeclarableInterval = "the reconcile interval is not one a five-field cron expression can express"
	// TriggerUndeclarableParked: the source is not on its schedule. A source
	// the compiler parked, or one an operator suspended, keeps its configured
	// interval and is held off the schedule by a cleared next-reconcile
	// instant — the same switch the park and restore paths have always used.
	// It declares no trigger rather than a disabled one: a declaration an
	// installing module would admit occurrences from is not the place to say
	// "but not right now".
	TriggerUndeclarableParked = "the source is parked and is not on its reconcile schedule"
)

// DeclaredTriggerSchedule is the clock a release declares, in the vocabulary
// the executing module validates. The field names are its contract, not this
// host's invention, and the JSON tags are what a composition folds into the
// installation document — so a rename here is a release that will not install
// rather than a schedule that quietly stops.
type DeclaredTriggerSchedule struct {
	Cron           string `json:"cron"`
	Timezone       string `json:"timezone"`
	MissedRun      string `json:"missed_run"`
	Overlap        string `json:"overlap"`
	DeadlineMillis int64  `json:"deadline_millis"`
	Input          []byte `json:"input"`
}

// DeclaredTriggerIdentity is the identity a declared trigger's occurrences run
// under. Exactly one field is set. Two fields rather than a discriminated
// union because that is the shape the executing module reads, and a host that
// re-spelled it would be answering a contract nobody declared.
type DeclaredTriggerIdentity struct {
	// SourceDelegation is the grant reference: a connected source whose
	// delegation this host owns and re-checks on every mint.
	SourceDelegation string `json:"source_delegation,omitempty"`
	// InstallationBinding is one of the release's installed operation
	// bindings, minted from its headless scopes under the installation's own
	// principal.
	InstallationBinding string `json:"installation_binding,omitempty"`
}

// Declared reports whether exactly one arm is named. A trigger that names no
// identity is refused at install, and one that names both is a declaration
// with two answers.
func (i DeclaredTriggerIdentity) Declared() bool {
	return (i.SourceDelegation == "") != (i.InstallationBinding == "")
}

// Reference reports whether the grant-reference arm is the one named.
func (i DeclaredTriggerIdentity) Reference() bool { return i.SourceDelegation != "" }

// SourceTriggerDeclaration is the reconcile schedule this host declares for
// one source, read back whole.
type SourceTriggerDeclaration struct {
	Release   string
	Operation string
	// Schedule and Identity are zero when UndeclarableReason is set.
	Schedule DeclaredTriggerSchedule
	Identity DeclaredTriggerIdentity
	Digest   string
	// LastOccurrenceAt is the most recent reconcile this host recorded for the
	// source, whatever admitted it. NextOccurrenceAt is the next instant the
	// declared cron comes due, computed from the declaration — never a stored
	// cursor, because the cursor belongs to the module that fires the trigger.
	LastOccurrenceAt   *time.Time
	NextOccurrenceAt   *time.Time
	UndeclarableReason string
}

// Declarable reports whether this source declares a trigger at all.
func (d SourceTriggerDeclaration) Declarable() bool { return d.UndeclarableReason == "" }

// TriggerDelegationReference is one delegation reference as an operator binds
// it to a declared release's triggers.
type TriggerDelegationReference struct {
	// Reference is the value a release's trigger names as its source
	// delegation: the source's id. The host resolves the active delegation
	// from it on every mint, so a reference keeps resolving across a
	// reconnect that replaces the row, and never names a revoked one.
	Reference        string
	DelegationID     string
	Module           string
	Binding          string
	Audience         string
	Tenant           string
	OwnerPrincipalID string
	// Scopes is what occurrences may do, as `kind:action[:resource]`: exactly
	// the binding's declared delegation scopes. Empty when the module no
	// longer declares the binding the delegation was recorded against — the
	// delegation is then already unusable, and saying so is the point.
	Scopes        []string
	Active        bool
	CreatedAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

// TriggerChildContext is one child Work Context this host minted against a
// reference, as the audit trail recorded it. There is no other record to read:
// a capability lives about a minute and nothing caches one.
type TriggerChildContext struct {
	// Record is the trail row this child was read from, which is what a child
	// is identified by: nothing stores a capability.
	Record       string
	DelegationID string
	Module       string
	Binding      string
	Audience     string
	Scopes       []string
	Lookup       bool
	MintedAt     time.Time
}

// ErrTriggerSourceNotFound reports a source that is not in the organization.
var ErrTriggerSourceNotFound = errors.New("datasource source not found")

// datasourceTriggerCron spells one reconcile interval as a five-field cron
// expression, or reports that it cannot.
//
// The grammar is narrower than a duration, and pretending otherwise is how a
// schedule silently becomes a different schedule. Three shapes are expressible
// and the rest are refused by name:
//
//   - a whole number of minutes that divides an hour: `*/M * * * *`. Any other
//     minute count would restart at the top of each hour, so `*/25` fires at
//     :00, :25, :50, then :00 again — twenty-five minutes asked for, ten
//     delivered once an hour.
//   - a whole number of hours that divides a day: `<m> */H * * *`, for the same
//     reason one step up.
//   - exactly a day: `<m> <h> * * *`.
//
// The minute (and, for a daily interval, the hour) is derived from the source's
// own id rather than fixed at zero. Every source of a provider shares one
// interval, so a fixed minute would have every source in a deployment come due
// in the same second — a thundering herd on one provider, by construction. The
// derivation is deterministic, so a source's declared instant does not move
// between two reads of the same declaration.
func datasourceTriggerCron(interval time.Duration, sourceID string) (string, string) {
	if interval <= 0 {
		return "", TriggerUndeclarableNoSchedule
	}
	spread := datasourceTriggerSpread(sourceID)
	switch {
	case interval < time.Hour:
		if interval%time.Minute != 0 {
			return "", TriggerUndeclarableInterval
		}
		minutes := int(interval / time.Minute)
		if 60%minutes != 0 {
			return "", TriggerUndeclarableInterval
		}
		return fmt.Sprintf("*/%d * * * *", minutes), ""
	case interval == 24*time.Hour:
		return fmt.Sprintf("%d %d * * *", spread%60, (spread/60)%24), ""
	case interval < 24*time.Hour:
		if interval%time.Hour != 0 {
			return "", TriggerUndeclarableInterval
		}
		hours := int(interval / time.Hour)
		if 24%hours != 0 {
			return "", TriggerUndeclarableInterval
		}
		return fmt.Sprintf("%d */%d * * *", spread%60, hours), ""
	default:
		// Beyond a day a five-field cron names days of the month or days of the
		// week, neither of which is "every N days". Refused rather than
		// approximated.
		return "", TriggerUndeclarableInterval
	}
}

// datasourceTriggerSpread is the stable offset one source's declared instant
// takes inside its period: a digest of the source id, read as minutes.
func datasourceTriggerSpread(sourceID string) int {
	sum := sha256.Sum256(append([]byte("codefly.saas.datasource-trigger-spread.v1\x00"), sourceID...))
	return int(binary.BigEndian.Uint32(sum[:4]) % (24 * 60))
}

// datasourceTriggerDigest fingerprints a declaration so an interval edit is
// visibly a different declaration rather than the same one with different
// contents. It covers the release, the operation, the schedule and the
// identity — everything a composition installs — and nothing that only this
// host reads.
func datasourceTriggerDigest(declaration SourceTriggerDeclaration) string {
	encoded, err := json.Marshal(struct {
		Release   string                  `json:"release"`
		Operation string                  `json:"operation"`
		Schedule  DeclaredTriggerSchedule `json:"schedule"`
		Identity  DeclaredTriggerIdentity `json:"identity"`
	}{declaration.Release, declaration.Operation, declaration.Schedule, declaration.Identity})
	if err != nil {
		// Strings, an int64 and a byte slice; they cannot fail to encode.
		panic("datasource trigger: declaration does not encode: " + err.Error())
	}
	sum := sha256.Sum256(append([]byte("codefly.saas.datasource-trigger-declaration.v1\x00"), encoded...))
	return hex.EncodeToString(sum[:])
}

// datasourceTriggerInput is the input every occurrence of one source's trigger
// is admitted with. A schedule has no arrival to take an input from, so the
// declaration carries it, and it is exactly the operation's own request shape
// minus the effect identity — which is the occurrence's, not the
// declaration's.
func datasourceTriggerInput(orgID, sourceID string) []byte {
	encoded, err := json.Marshal(struct {
		OrgID    string `json:"org_id"`
		SourceID string `json:"source_id"`
	}{orgID, sourceID})
	if err != nil {
		panic("datasource trigger: input does not encode: " + err.Error())
	}
	return encoded
}

// DeclareSourceTrigger builds the trigger this host declares for one source,
// under the identity its active delegation gives it.
//
// The identity is the reference arm whenever a reference exists, and that is
// not a preference between two equal options: a reference is revocable and
// re-checked on every occurrence, which is what lets an occurrence's run
// outlive the capability that admitted it. The installation-binding arm is the
// fallback — an ordinary short-lived token admission — and it is named only
// when no delegation is there to reference. A source with neither declares a
// schedule with no identity, which an installing module refuses rather than
// running as itself.
func DeclareSourceTrigger(source *DatasourceSource, references []TriggerDelegationReference, lastOccurrence *time.Time) SourceTriggerDeclaration {
	declaration := SourceTriggerDeclaration{
		Release:          DatasourceReconcileRelease,
		Operation:        DatasourceReconcileOperation,
		LastOccurrenceAt: lastOccurrence,
	}
	if source == nil {
		declaration.UndeclarableReason = TriggerUndeclarableNoSchedule
		return declaration
	}
	cron, reason := datasourceTriggerCron(source.ReconcileInterval, source.ID)
	if reason != "" {
		declaration.UndeclarableReason = reason
		return declaration
	}
	// Enablement is the stored next-reconcile instant's PRESENCE, never its
	// value: the park, restore and cursor paths maintain it, and a declaration
	// that read it as a time would be a second answer to "when" beside the
	// cron it declares.
	if source.Status != DatasourceStatusActive || source.NextReconcileAt == nil {
		declaration.UndeclarableReason = TriggerUndeclarableParked
		return declaration
	}
	declaration.Schedule = DeclaredTriggerSchedule{
		Cron:           cron,
		Timezone:       DatasourceTriggerTimezone,
		MissedRun:      DatasourceTriggerMissedRun,
		Overlap:        DatasourceTriggerOverlap,
		DeadlineMillis: DatasourceTriggerDeadline.Milliseconds(),
		Input:          datasourceTriggerInput(source.OrgID, source.ID),
	}
	for _, reference := range references {
		if reference.Active && reference.Reference != "" {
			declaration.Identity = DeclaredTriggerIdentity{SourceDelegation: reference.Reference}
			break
		}
	}
	declaration.Digest = datasourceTriggerDigest(declaration)
	if next, ok := nextCronOccurrence(declaration.Schedule, time.Now().UTC()); ok {
		declaration.NextOccurrenceAt = &next
	}
	return declaration
}

// nextCronOccurrence is the next instant a declared schedule comes due, for a
// reader. It understands exactly the three shapes datasourceTriggerCron emits
// and nothing else, which is why it is not a cron library: the authoritative
// evaluation is the executing module's, and a second general implementation
// here would be a second answer to one question.
func nextCronOccurrence(schedule DeclaredTriggerSchedule, from time.Time) (time.Time, bool) {
	fields := strings.Fields(schedule.Cron)
	if len(fields) != 5 {
		return time.Time{}, false
	}
	from = from.UTC().Truncate(time.Minute)
	minute, hour := fields[0], fields[1]
	switch {
	case strings.HasPrefix(minute, "*/") && hour == "*":
		step, err := strconv.Atoi(strings.TrimPrefix(minute, "*/"))
		if err != nil || step <= 0 {
			return time.Time{}, false
		}
		next := from.Add(time.Minute)
		for next.Minute()%step != 0 {
			next = next.Add(time.Minute)
		}
		return next, true
	case strings.HasPrefix(hour, "*/"):
		step, err := strconv.Atoi(strings.TrimPrefix(hour, "*/"))
		at, atErr := strconv.Atoi(minute)
		if err != nil || atErr != nil || step <= 0 || at < 0 || at > 59 {
			return time.Time{}, false
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), at, 0, 0, time.UTC)
		for !next.After(from) || next.Hour()%step != 0 {
			next = next.Add(time.Hour).Truncate(time.Hour).Add(time.Duration(at) * time.Minute)
		}
		return next, true
	default:
		at, atErr := strconv.Atoi(minute)
		on, onErr := strconv.Atoi(hour)
		if atErr != nil || onErr != nil {
			return time.Time{}, false
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), on, at, 0, 0, time.UTC)
		if !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next, true
	}
}

// SourceTriggerIdentities reads the delegation references an operator may bind
// to one source's triggers, revoked ones included, newest first.
//
// The audience and scopes are resolved through the LIVE authority of the module
// the delegation names — the same path a mint takes — and not from the declared
// ceiling, nor from anything stored with the delegation. Three different things
// can make a recorded delegation confer nothing: the binding's audience or
// scopes moved, the module holds no current installation in that organization,
// or the declaration is gone. Each of them is a delegation the next mint will
// refuse, and an operator reading a reference needs to see that BEFORE they
// bind it to a release's triggers rather than after an occurrence is refused.
// A read that reported the declared ceiling would show authority that does not
// exist.
//
// A reference whose authority cannot be resolved comes back with no audience
// and no scopes rather than being omitted: the row is what an operator revokes
// and what the trail refers to, and hiding it would hide the problem too.
func (s *Service) SourceTriggerIdentities(ctx context.Context, orgID, sourceID string) ([]TriggerDelegationReference, error) {
	// One fence spans the whole read, not one per delegation. Every reference
	// in the answer is resolved against the same registry revision, so a
	// registry installed mid-read cannot hand one row's authority to another's
	// — which is the same reason a mint holds the fence across its decision and
	// its effect.
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	delegations, err := s.ListSourceDelegations(ctx, orgID, sourceID, true)
	if err != nil {
		return nil, err
	}
	references := make([]TriggerDelegationReference, 0, len(delegations))
	for _, delegation := range delegations {
		reference := TriggerDelegationReference{
			Reference:        delegation.SourceID,
			DelegationID:     delegation.ID,
			Module:           delegation.ModulePrefix,
			Binding:          delegation.BindingID,
			Tenant:           delegation.OrgID,
			OwnerPrincipalID: delegation.PrincipalID,
			Active:           delegation.Active(),
			CreatedAt:        delegation.CreatedAt,
			RevokedAt:        delegation.RevokedAt,
			RevokedReason:    delegation.RevokedReason,
		}
		authority, authErr := s.AuthorizeModuleCapability(ctx, ModuleCaller{
			PrincipalID: ModulePrincipalID(delegation.ModulePrefix),
			BoundOrg:    delegation.OrgID,
		})
		if authErr == nil && authority != nil {
			if binding, current := authority.Grant.currentSourceDelegationBinding(delegation); current {
				reference.Audience = binding.Audience
				reference.Scopes = operationScopeGrants(binding.SourceDelegationScopes)
			}
		}
		references = append(references, reference)
	}
	return sortedTriggerReferences(references), nil
}

// SourceTriggerChildren reads the child Work Contexts minted against one
// source's references, newest first, from the trail that records every mint.
func (s *Service) SourceTriggerChildren(ctx context.Context, orgID, sourceID string, limit int32) ([]TriggerChildContext, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	entries, _, _, err := s.QueryAuditLog(ctx, AuditQuery{
		OrgID:      orgID,
		EventType:  string(EventSourceDelegationUsed),
		Resource:   "datasource",
		ResourceID: sourceID,
		PageSize:   limit,
	})
	if err != nil {
		return nil, err
	}
	children := make([]TriggerChildContext, 0, len(entries))
	for _, entry := range entries {
		child := TriggerChildContext{Record: entry.ID, MintedAt: entry.CreatedAt}
		child.DelegationID, _ = entry.Payload["delegation_id"].(string)
		child.Module, _ = entry.Payload["module"].(string)
		child.Binding, _ = entry.Payload["binding_id"].(string)
		child.Audience, _ = entry.Payload["audience"].(string)
		child.Lookup, _ = entry.Payload["lookup"].(bool)
		child.Scopes = auditStringList(entry.Payload["scopes"])
		children = append(children, child)
	}
	return children, nil
}

// auditStringList reads a recorded list of strings back out of a payload. A
// payload round-trips through JSON, so a []string is read back as []any.
func auditStringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return slices.Clone(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, element := range typed {
			if text, ok := element.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// GetSourceTriggerDeclaration answers everything a reader needs about one
// source's reconcile trigger: the declaration, the references an operator may
// bind to it, and the children already minted against them.
func (s *Service) GetSourceTriggerDeclaration(ctx context.Context, orgID, sourceID string) (SourceTriggerDeclaration, []TriggerDelegationReference, []TriggerChildContext, error) {
	source, err := s.GetDatasourceSource(ctx, orgID, sourceID)
	if err != nil {
		return SourceTriggerDeclaration{}, nil, nil, err
	}
	references, err := s.SourceTriggerIdentities(ctx, orgID, sourceID)
	if err != nil {
		return SourceTriggerDeclaration{}, nil, nil, err
	}
	children, err := s.SourceTriggerChildren(ctx, orgID, sourceID, 0)
	if err != nil {
		return SourceTriggerDeclaration{}, nil, nil, err
	}
	return DeclareSourceTrigger(source, references, source.LastSyncedAt), references, children, nil
}

// DelegateSourceTriggers records the delegation a declared release's triggers
// run under, for an already-connected source, without reconnecting it.
//
// It is the one act of binding that was otherwise only available at connect. A
// delegation is recorded per module whose one binding declares
// source_delegation_scopes, and only at the moment a person connects or
// reconnects a source — so a release installed afterwards, or a module that
// declared its binding afterwards, finds no reference to name and nothing
// backfills one. Before this, the only way to get one was to make an
// administrator reconnect every source.
//
// It grants nothing new. The authority is the calling administrator's own, the
// scopes are the binding's declared ones, and the facts are re-checked on every
// mint exactly as for a delegation a connect recorded. What it changes is who
// has to act and when, which is an operational fact and not an authorization
// one.
func (s *Service) DelegateSourceTriggers(ctx context.Context, actorID, orgID, sourceID string) ([]TriggerDelegationReference, error) {
	orgID, sourceID = strings.TrimSpace(orgID), strings.TrimSpace(sourceID)
	if orgID == "" || sourceID == "" {
		return nil, errors.New("org id and source id are required")
	}
	// There is deliberately no pre-check for "does any module accept a
	// delegation here". recordSourceDelegationsTx asks that question against
	// the registry it will record from, inside the transaction, and records
	// nothing when the answer is none; asking it here as well would be a
	// second answer that can disagree with the first. The caller then reads
	// the empty reference list as the deployment's answer.
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		source, err := s.store.GetDatasourceSource(ctx, orgID, sourceID)
		if err != nil {
			return err
		}
		if source == nil {
			return ErrTriggerSourceNotFound
		}
		return s.recordSourceDelegationsTx(ctx, actorID, orgID, sourceID)
	}); err != nil {
		return nil, err
	}
	return s.SourceTriggerIdentities(ctx, orgID, sourceID)
}

// sortedTriggerReferences orders references the way an operator reads them:
// active first, then newest.
func sortedTriggerReferences(references []TriggerDelegationReference) []TriggerDelegationReference {
	out := slices.Clone(references)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}
