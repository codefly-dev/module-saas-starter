package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"accounts/pkg/eventcatalog"
)

// Solution-declared audit event types.
//
// A registered solution declares the audit event types it emits in the one
// declaration it already registers: the dashboard graph its registration
// manifest carries. An event in that graph's `events` section that carries
// `fields` is a type the solution owns, with those typed payload fields; an
// event without `fields` binds a type that already exists, exactly as before.
// There is no second artifact — no contribution file, no proto — so a solution
// written in any language declares its types in the file it already writes.
//
// The host admits declared types when the frontend half of the registration is
// written, inside the same transaction as the write. A declaration the audit
// registry refuses therefore refuses the whole write: the registration that was
// last admitted keeps serving, and nothing is ever stored that the catalog did
// not accept.
//
// An admitted type is a row of audit_event_types owned by "solution:<id>" — the
// same table the code-owned catalog projects into, and the one
// audit_events.event_type is a foreign key into. That is what lets an emitted
// event of that type be stored at all; the row's payload_schema is the typed
// field set, the same JSON Schema projection the code catalog stores.
//
// The rules, all fail-closed:
//
//   - A type is `<namespace>.<aggregate>.<event>`: at least three segments, each
//     starting with a letter — the shape audit_events admits.
//   - A reserved namespace is refused: `saas`, the platform's own.
//   - A solution declares only into namespaces the operator bound to it: the
//     `namespaces` of its own MODULE_PRINCIPALS entry, keyed by its solution id
//     — the same grant a declared type's emission is checked against. A
//     solution with no entry admits nothing. The registration credential alone
//     therefore claims nothing: a namespace is the operator's to hand out.
//   - A namespace belongs to one producer. Another solution still bound to it,
//     or the code catalog, holding any type there refuses the declaration, and
//     so does a namespace the composed event catalog publishes domain events
//     under (eventcatalog.IsPublishedNamespace). This is the naming law domain
//     events already follow (EVENTS.md): two producers can never mint the same
//     name.
//   - Ownership follows the binding, and that is the release path. When the
//     operator removes a namespace from the holding solution's entry and binds
//     it to another, the next admission by the newly bound solution takes the
//     namespace over — every type in it, historical schemas included, so the
//     additive rule keeps applying — and the registration's audit event records
//     the takeover. Releasing through any other authority would not hold: while
//     the binding stands, the holder's next registration would claim it back.
//   - A type is declared at most once in one declaration.
//   - Field names are snake_case, unique within the type, and never `solution`,
//     which the host stamps on every emitted payload.
//   - A field kind is one of the registry's kinds, `number` (finite) included;
//     `enum` names its values and nothing else does. A field marked `pii` is
//     stripped from every path that sends an event outside the audit store
//     (AuditEventResolver).
//   - Re-registering an identical declaration changes nothing. A changed field
//     set is additive only: a field may be added, and an enum may gain values,
//     and a field may become `pii`, but no field is removed, retyped, loses a
//     value or stops being `pii` — rows already written under the earlier set
//     must still mean what they meant.
//   - A type is never removed. A declaration that stops listing it leaves it
//     admitted and owned, because historical rows keep referencing it.
//   - A type declares how far its events may travel: `tenant` (the default) or
//     `external`. Only an `external` type is ever delivered to a tenant's
//     outbound webhook endpoint. Eligibility to leave the platform takes two
//     keys and neither alone suffices: the producer declares which of its facts
//     are customer-facing, and the operator marks the namespace externally
//     deliverable (`external_namespaces` of the same MODULE_PRINCIPALS entry
//     that binds the namespace). An `external` declaration in a namespace the
//     operator did not mark is refused, naming both.
//   - Visibility is fixed at admission. A re-declaration that changes it is
//     refused: narrowing it would silently stop deliveries to endpoints already
//     subscribed, and widening it would start sending outside the platform
//     facts a tenant's endpoint never agreed to receive under that name. A
//     producer that needs the other visibility declares another type.

// SolutionAuditOwnerPrefix prefixes the owner of every solution-declared audit
// event type, followed by the solution id — the same subject form the
// registration credential proves (`sub=solution:<id>`). The code-owned catalog
// never uses it, which is how the startup projection sync tells the two apart.
const SolutionAuditOwnerPrefix = "solution:"

// DeclaredAuditEventVersion is the version every solution-declared type
// carries. A field set only ever grows, so no change requires a new version.
const DeclaredAuditEventVersion = 1

// How far an event of a declared type may travel. The two values are the
// composed event catalog's own vocabulary for the same fact (catalog_gen.go
// `Visibility`), narrowed to what a declaration can mean: an audit record is
// always readable by its own tenant, so the catalog's `internal` has no
// reading here.
const (
	// AuditVisibilityTenant keeps events inside the platform. It is the value
	// a declaration that says nothing gets: eligibility to leave is granted by
	// declaration, never by omission.
	AuditVisibilityTenant = "tenant"
	// AuditVisibilityExternal additionally admits delivery to a tenant's
	// outbound webhook endpoint.
	AuditVisibilityExternal = "external"
)

const (
	maxDeclaredAuditEventTypes     = 64
	maxDeclaredAuditEventFields    = 32
	maxDeclaredAuditEventEnumItems = 64
	// maxDeclaredAuditEventTypeLen is the bound ModuleEmitAuditEventRequest
	// puts on event_type: a longer type could be admitted and never emitted.
	maxDeclaredAuditEventTypeLen = 128
	maxDeclaredAuditFieldNameLen = 64
	maxDeclaredAuditTextLen      = 1024
	// hostStampedAuditField is the payload field the host sets on every
	// module-emitted event from the emitting scope (ModuleEmitAuditEvent).
	hostStampedAuditField = "solution"
)

var (
	// ErrSolutionAuditDeclarationRejected is returned when the event types a
	// registration declares cannot be admitted. The wrapped message names the
	// event and the rule it broke.
	ErrSolutionAuditDeclarationRejected = errors.New("solution audit event declaration rejected")
	// ErrSolutionAuditNamespaceOwned is returned, wrapped by
	// ErrSolutionAuditDeclarationRejected, when a declared type's namespace is
	// already held by another producer.
	ErrSolutionAuditNamespaceOwned = errors.New("audit event namespace is owned by another producer")
	// ErrSolutionAuditNamespaceUnbound is returned, wrapped by
	// ErrSolutionAuditDeclarationRejected, when a declared type's namespace is
	// not among the namespaces the operator bound to the declaring solution.
	ErrSolutionAuditNamespaceUnbound = errors.New("audit event namespace is not bound to the declaring solution")
	// ErrSolutionAuditNamespaceNotExternal is returned, wrapped by
	// ErrSolutionAuditDeclarationRejected, when a type is declared with
	// external visibility in a namespace the operator did not mark externally
	// deliverable. The producer's declaration is one key; this is the other.
	ErrSolutionAuditNamespaceNotExternal = errors.New("audit event namespace is not granted external delivery")
	// ErrAuditCatalogCollision is returned by the startup projection sync when
	// a code-catalog type takes the name, or the namespace, of a type a
	// solution declared. It is a release that cannot boot as built: the
	// operator must release the namespace, or the catalog must rename.
	ErrAuditCatalogCollision = errors.New("audit catalog collides with a solution-declared type")
)

var (
	declaredAuditEventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$`)
	declaredAuditFieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// declarableAuditFieldKinds is every kind a declaration may name.
var declarableAuditFieldKinds = map[FieldKind]bool{
	FieldString:      true,
	FieldUUID:        true,
	FieldInt:         true,
	FieldNumber:      true,
	FieldBool:        true,
	FieldEnum:        true,
	FieldStringArray: true,
}

// DeclaredAuditEventType is one audit event type a registered solution owns.
// Fields are the declared payload fields, sorted by name; the host-stamped
// `solution` field is implied and never listed.
type DeclaredAuditEventType struct {
	Type        EventType
	Namespace   string
	SolutionID  string
	Description string
	// Visibility is AuditVisibilityTenant or AuditVisibilityExternal; it is
	// never empty on a validated declaration, so a zero value reaching a gate
	// is a type that skipped validation rather than one that opted out.
	Visibility string
	Fields     []PayloadField
}

// ExternallyDeliverable reports whether an event of this type may be delivered
// to a tenant's outbound webhook endpoint.
func (d DeclaredAuditEventType) ExternallyDeliverable() bool {
	return d.Visibility == AuditVisibilityExternal
}

// SolutionAuditOwner is the audit_event_types owner of a solution's types.
func SolutionAuditOwner(solutionID string) string {
	return SolutionAuditOwnerPrefix + solutionID
}

// SolutionIDFromAuditOwner reports the solution an owner names, and whether it
// names one at all (a code-owned type's owner is a service name).
func SolutionIDFromAuditOwner(owner string) (string, bool) {
	id, ok := strings.CutPrefix(owner, SolutionAuditOwnerPrefix)
	return id, ok && id != ""
}

// auditEventNamespace is the leading segment of an event type.
func auditEventNamespace(t EventType) string {
	namespace, _, _ := strings.Cut(string(t), ".")
	return namespace
}

// isReservedAuditNamespace reports whether no solution may declare types in a
// namespace. `saas` is the platform's own, reserved in the module package
// against every downstream contribution.
func isReservedAuditNamespace(namespace string) bool {
	return namespace == AuditNamespace
}

// isDeclarableAuditEventType reports whether a type has the shape a solution
// could have declared. The emission path uses it to refuse an unregistered
// type without a database read.
func isDeclarableAuditEventType(t EventType) bool {
	return len(t) <= maxDeclaredAuditEventTypeLen &&
		declaredAuditEventTypePattern.MatchString(string(t)) &&
		!isReservedAuditNamespace(auditEventNamespace(t))
}

// validationFields is the field set an emitted payload is checked against: the
// declared fields plus the host-stamped solution, which every module-emitted
// payload carries.
func (d DeclaredAuditEventType) validationFields() []PayloadField {
	fields := make([]PayloadField, 0, len(d.Fields)+1)
	fields = append(fields, PayloadField{Name: hostStampedAuditField, Kind: FieldString, Required: true})
	return append(fields, d.Fields...)
}

// PayloadSchemaJSON is the JSON Schema stored in audit_event_types.payload_schema
// for a declared type: the same projection the code catalog stores, carrying
// the host-stamped solution field and the declared description.
func (d DeclaredAuditEventType) PayloadSchemaJSON() []byte {
	definition := AuditEventDefinition{Type: d.Type, Fields: d.validationFields()}
	schema := definition.payloadJSONSchema()
	if d.Description != "" {
		schema["description"] = d.Description
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// Definition projects a declared type onto the definition shape the catalog
// listing uses. It is observational: a module emission is its own operation,
// written on the emitter's own transaction.
func (d DeclaredAuditEventType) Definition() AuditEventDefinition {
	return AuditEventDefinition{
		Type:        d.Type,
		Namespace:   d.Namespace,
		Version:     DeclaredAuditEventVersion,
		Category:    CategorySolution,
		Owner:       SolutionAuditOwner(d.SolutionID),
		Description: d.Description,
		Durability:  DurabilityObservational,
		Visibility:  d.Visibility,
		// The whole payload the type carries, the host-stamped solution
		// included, so a declared type validates and redacts exactly as the
		// row's stored schema says.
		Fields: d.validationFields(),
	}
}

// storedDeclaredAuditSchema is the subset of the stored JSON Schema a declared
// type is read back from. Only PayloadSchemaJSON writes it.
type storedDeclaredAuditSchema struct {
	Description string `json:"description"`
	Properties  map[string]struct {
		Type   string   `json:"type"`
		Format string   `json:"format"`
		Enum   []string `json:"enum"`
		PII    bool     `json:"x-pii"`
		Items  *struct {
			Type string `json:"type"`
		} `json:"items"`
	} `json:"properties"`
}

// DeclaredAuditEventTypeFromSchema reads a declared type back from its stored
// row. It inverts PayloadSchemaJSON exactly and refuses anything else, so a row
// that was not written by admission never validates a payload.
func DeclaredAuditEventTypeFromSchema(eventType EventType, namespace, owner, visibility string, schema []byte) (DeclaredAuditEventType, error) {
	solutionID, ok := SolutionIDFromAuditOwner(owner)
	if !ok {
		return DeclaredAuditEventType{}, fmt.Errorf("audit: event type %q is not owned by a solution", eventType)
	}
	// The column is the gate's input, so an unreadable value is refused rather
	// than defaulted: defaulting one way would stop a tenant's deliveries and
	// the other way would start sending rows outside the platform, and neither
	// is a decision a scan may make.
	if visibility != AuditVisibilityTenant && visibility != AuditVisibilityExternal {
		return DeclaredAuditEventType{}, fmt.Errorf("audit: event type %q has unreadable visibility %q", eventType, visibility)
	}
	var stored storedDeclaredAuditSchema
	if err := json.Unmarshal(schema, &stored); err != nil {
		return DeclaredAuditEventType{}, fmt.Errorf("audit: event type %q has an unreadable payload schema: %w", eventType, err)
	}
	declared := DeclaredAuditEventType{
		Type:        eventType,
		Namespace:   namespace,
		SolutionID:  solutionID,
		Description: stored.Description,
		Visibility:  visibility,
	}
	for name, property := range stored.Properties {
		if name == hostStampedAuditField {
			continue
		}
		field := PayloadField{Name: name, PII: property.PII}
		switch {
		case property.Type == "string" && property.Format == "uuid":
			field.Kind = FieldUUID
		case property.Type == "string" && len(property.Enum) > 0:
			field.Kind = FieldEnum
			field.Enum = append([]string(nil), property.Enum...)
		case property.Type == "string":
			field.Kind = FieldString
		case property.Type == "integer":
			field.Kind = FieldInt
		case property.Type == "number":
			field.Kind = FieldNumber
		case property.Type == "boolean":
			field.Kind = FieldBool
		case property.Type == "array" && property.Items != nil && property.Items.Type == "string":
			field.Kind = FieldStringArray
		default:
			return DeclaredAuditEventType{}, fmt.Errorf("audit: event type %q field %q has an unreadable kind", eventType, name)
		}
		declared.Fields = append(declared.Fields, field)
	}
	sortPayloadFields(declared.Fields)
	return declared, nil
}

func sortPayloadFields(fields []PayloadField) {
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
}

func declarationRejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSolutionAuditDeclarationRejected, fmt.Sprintf(format, args...))
}

// declaredEventJSON is one entry of the manifest's `dashboard.events`. The rest
// of the manifest is the frontend's to validate; only a declaration's own
// fields are read here, and they are read strictly.
type declaredEventJSON struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Visibility  string          `json:"visibility"`
	Fields      json.RawMessage `json:"fields"`
}

type declaredFieldJSON struct {
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
	Values *[]string `json:"values"`
	// PII marks a personally identifying field: it is stripped from every
	// path that sends an event outside the audit store.
	PII bool `json:"pii"`
}

// ParseDeclaredAuditEventTypes extracts the audit event types a registration
// manifest declares, validated against every rule that needs no database. A
// manifest with no dashboard, or whose events carry no fields, declares none.
func ParseDeclaredAuditEventTypes(solutionID, manifest string) ([]DeclaredAuditEventType, error) {
	var document struct {
		Dashboard *struct {
			Events []json.RawMessage `json:"events"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal([]byte(manifest), &document); err != nil {
		return nil, declarationRejected("the manifest's dashboard events cannot be read: %v", err)
	}
	if document.Dashboard == nil {
		return nil, nil
	}
	var inputs []AuditEventTypeDeclaration
	for _, raw := range document.Dashboard.Events {
		var event declaredEventJSON
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, declarationRejected("a dashboard event cannot be read: %v", err)
		}
		if event.Fields == nil {
			// Binds an existing type; nothing to admit.
			continue
		}
		fields, err := decodeDeclaredAuditFields(event.Type, event.Fields)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, AuditEventTypeDeclaration{
			Type: event.Type, Description: event.Description,
			Visibility: event.Visibility, Fields: fields,
		})
	}
	return ValidateAuditEventTypeDeclarations(solutionID, inputs)
}

// AuditEventTypeDeclaration is one audit event type as its producer declares
// it, before validation: a solution's manifest (`dashboard.events[].fields`)
// and a composed module's DeclareAuditEventTypes call both arrive in this
// shape, so both are held to the one set of rules below.
type AuditEventTypeDeclaration struct {
	Type        string
	Description string
	// Visibility is "tenant", "external", or empty for the default (tenant).
	// The operator's grant is checked at admission, which is the only place
	// the declaring principal's MODULE_PRINCIPALS entry is in hand.
	Visibility string
	Fields     []AuditFieldDeclaration
}

// AuditFieldDeclaration is one declared payload field. Kind is a FieldKind's
// name; Values is required for an enum and refused otherwise; PII strips the
// field from every path that sends an event outside the audit store.
type AuditFieldDeclaration struct {
	Name   string
	Kind   string
	Values []string
	PII    bool
}

// ValidateAuditEventTypeDeclarations applies every rule a declaration is held
// to that needs no database, and returns the declared types sorted by type.
// It is the one validator both declaration paths share.
func ValidateAuditEventTypeDeclarations(solutionID string, inputs []AuditEventTypeDeclaration) ([]DeclaredAuditEventType, error) {
	var (
		declared []DeclaredAuditEventType
		seen     = map[EventType]bool{}
	)
	for _, event := range inputs {
		eventType := EventType(event.Type)
		if !declaredAuditEventTypePattern.MatchString(event.Type) || len(event.Type) > maxDeclaredAuditEventTypeLen {
			return nil, declarationRejected(
				"event type %q must be <namespace>.<aggregate>.<event> in lowercase snake_case segments, at most %d characters",
				event.Type, maxDeclaredAuditEventTypeLen)
		}
		namespace := auditEventNamespace(eventType)
		if isReservedAuditNamespace(namespace) {
			return nil, declarationRejected("event type %q is in the reserved namespace %q", event.Type, namespace)
		}
		if _, registered := LookupAuditEvent(eventType); registered {
			return nil, declarationRejected("event type %q is already a registered platform type", event.Type)
		}
		if seen[eventType] {
			return nil, declarationRejected("event type %q is declared more than once", event.Type)
		}
		seen[eventType] = true
		if len(event.Description) > maxDeclaredAuditTextLen {
			return nil, declarationRejected("event type %q description exceeds %d characters", event.Type, maxDeclaredAuditTextLen)
		}
		visibility, err := validateDeclaredAuditVisibility(event.Type, event.Visibility)
		if err != nil {
			return nil, err
		}
		fields, err := validateDeclaredAuditFields(event.Type, event.Fields)
		if err != nil {
			return nil, err
		}
		declared = append(declared, DeclaredAuditEventType{
			Type:        eventType,
			Namespace:   namespace,
			SolutionID:  solutionID,
			Description: strings.TrimSpace(event.Description),
			Visibility:  visibility,
			Fields:      fields,
		})
	}
	if len(declared) > maxDeclaredAuditEventTypes {
		return nil, declarationRejected("%d event types are declared; at most %d may be", len(declared), maxDeclaredAuditEventTypes)
	}
	sort.Slice(declared, func(i, j int) bool { return declared[i].Type < declared[j].Type })
	return declared, nil
}

// validateDeclaredAuditVisibility resolves a declaration's visibility. Empty is
// the default and means tenant; anything the vocabulary does not name is
// refused rather than treated as the default, so a misspelt "External" is a
// legible refusal instead of a type that silently never reaches an endpoint —
// which is the whole failure this declaration exists to make impossible.
func validateDeclaredAuditVisibility(eventType, visibility string) (string, error) {
	switch strings.TrimSpace(visibility) {
	case "":
		return AuditVisibilityTenant, nil
	case AuditVisibilityTenant:
		return AuditVisibilityTenant, nil
	case AuditVisibilityExternal:
		return AuditVisibilityExternal, nil
	default:
		return "", declarationRejected("event type %q declares visibility %q; it must be %q or %q",
			eventType, visibility, AuditVisibilityTenant, AuditVisibilityExternal)
	}
}

// decodeDeclaredAuditFields reads a manifest's `fields` array strictly: an
// unknown key on a field is refused, not ignored.
func decodeDeclaredAuditFields(eventType string, raw json.RawMessage) ([]AuditFieldDeclaration, error) {
	var entries []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &entries) != nil {
		return nil, declarationRejected("event type %q fields must be an array", eventType)
	}
	if len(entries) > maxDeclaredAuditEventFields {
		return nil, declarationRejected("event type %q declares %d fields; at most %d may be", eventType, len(entries), maxDeclaredAuditEventFields)
	}
	fields := make([]AuditFieldDeclaration, 0, len(entries))
	for _, entry := range entries {
		decoder := json.NewDecoder(bytes.NewReader(entry))
		decoder.DisallowUnknownFields()
		var field declaredFieldJSON
		if err := decoder.Decode(&field); err != nil {
			return nil, declarationRejected("event type %q has an unreadable field: %v", eventType, err)
		}
		declared := AuditFieldDeclaration{Name: field.Name, Kind: field.Kind, PII: field.PII}
		if field.Values != nil {
			// An empty list is still a list: for an enum it is refused as
			// declaring no values, and for any other kind as declaring values.
			declared.Values = append([]string{}, (*field.Values)...)
		}
		fields = append(fields, declared)
	}
	return fields, nil
}

func validateDeclaredAuditFields(eventType string, declared []AuditFieldDeclaration) ([]PayloadField, error) {
	if len(declared) > maxDeclaredAuditEventFields {
		return nil, declarationRejected("event type %q declares %d fields; at most %d may be", eventType, len(declared), maxDeclaredAuditEventFields)
	}
	fields := make([]PayloadField, 0, len(declared))
	names := map[string]bool{}
	for _, field := range declared {
		if !declaredAuditFieldNamePattern.MatchString(field.Name) || len(field.Name) > maxDeclaredAuditFieldNameLen {
			return nil, declarationRejected("event type %q field %q must be lowercase snake_case, at most %d characters",
				eventType, field.Name, maxDeclaredAuditFieldNameLen)
		}
		if field.Name == hostStampedAuditField {
			return nil, declarationRejected("event type %q may not declare %q: the host stamps it from the emitting scope",
				eventType, hostStampedAuditField)
		}
		if names[field.Name] {
			return nil, declarationRejected("event type %q declares field %q more than once", eventType, field.Name)
		}
		names[field.Name] = true
		kind := FieldKind(field.Kind)
		if !declarableAuditFieldKinds[kind] {
			return nil, declarationRejected("event type %q field %q has unknown kind %q", eventType, field.Name, field.Kind)
		}
		payloadField := PayloadField{Name: field.Name, Kind: kind, PII: field.PII}
		if kind == FieldEnum {
			values, err := declaredEnumValues(eventType, field)
			if err != nil {
				return nil, err
			}
			payloadField.Enum = values
		} else if field.Values != nil {
			return nil, declarationRejected("event type %q field %q declares values but is not an enum", eventType, field.Name)
		}
		fields = append(fields, payloadField)
	}
	sortPayloadFields(fields)
	return fields, nil
}

func declaredEnumValues(eventType string, field AuditFieldDeclaration) ([]string, error) {
	if len(field.Values) == 0 {
		return nil, declarationRejected("event type %q enum field %q must declare its values", eventType, field.Name)
	}
	if len(field.Values) > maxDeclaredAuditEventEnumItems {
		return nil, declarationRejected("event type %q enum field %q declares more than %d values", eventType, field.Name, maxDeclaredAuditEventEnumItems)
	}
	seen := map[string]bool{}
	values := make([]string, 0, len(field.Values))
	for _, value := range field.Values {
		if strings.TrimSpace(value) == "" || len(value) > maxDeclaredAuditTextLen {
			return nil, declarationRejected("event type %q enum field %q has an empty or oversized value", eventType, field.Name)
		}
		if seen[value] {
			return nil, declarationRejected("event type %q enum field %q lists %q more than once", eventType, field.Name, value)
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Strings(values)
	return values, nil
}

// checkAdditiveAuditFieldChange enforces that a re-declared type only grows:
// every field already admitted keeps its kind, and an enum keeps every value.
func checkAdditiveAuditFieldChange(admitted, declared DeclaredAuditEventType) error {
	next := make(map[string]PayloadField, len(declared.Fields))
	for _, field := range declared.Fields {
		next[field.Name] = field
	}
	for _, field := range admitted.Fields {
		replacement, kept := next[field.Name]
		if !kept {
			return declarationRejected("event type %q may not drop admitted field %q", declared.Type, field.Name)
		}
		if replacement.Kind != field.Kind {
			return declarationRejected("event type %q field %q is admitted as %s and may not become %s",
				declared.Type, field.Name, field.Kind, replacement.Kind)
		}
		// A field once marked PII stays PII: rows already written under it
		// hold personal data, and unmarking it would start exporting them.
		if field.PII && !replacement.PII {
			return declarationRejected("event type %q field %q is admitted as personally identifying and may not stop being so",
				declared.Type, field.Name)
		}
		if field.Kind == FieldEnum {
			values := make(map[string]bool, len(replacement.Enum))
			for _, value := range replacement.Enum {
				values[value] = true
			}
			for _, value := range field.Enum {
				if !values[value] {
					return declarationRejected("event type %q enum field %q may not drop admitted value %q",
						declared.Type, field.Name, value)
				}
			}
		}
	}
	return nil
}

func sameDeclaredAuditEventType(a, b DeclaredAuditEventType) bool {
	return bytes.Equal(a.PayloadSchemaJSON(), b.PayloadSchemaJSON())
}

// admitDeclaredAuditEventTypes records a solution's declared types and
// returns the namespaces it took over from a solution the operator no longer
// binds to them. It must run inside the registration write's control-plane
// transaction: the namespace locks it takes hold until that transaction ends,
// and a refusal here rolls the registration write back with it.
func (s *Service) admitDeclaredAuditEventTypes(ctx context.Context, solutionID string, declared []DeclaredAuditEventType) ([]string, error) {
	takenOver, _, err := s.admitDeclaredAuditEventTypesWritten(ctx, solutionID, declared)
	return takenOver, err
}

// admitDeclaredAuditEventTypesWritten is admitDeclaredAuditEventTypes that also
// reports which types it wrote, so a caller can tell an admission that changed
// the registry from one that re-declared what was already admitted.
func (s *Service) admitDeclaredAuditEventTypesWritten(ctx context.Context, solutionID string, declared []DeclaredAuditEventType) ([]string, []EventType, error) {
	if len(declared) == 0 {
		return nil, nil, nil
	}
	owner := SolutionAuditOwner(solutionID)
	// The binding is the solution's own principal entry. Keyed by the solution
	// id, it is the same entry — and the same `namespaces` — a declared type's
	// emission is authorized against, so admission can never hand a solution a
	// namespace it could not emit into.
	grant, bound := s.modulePrincipals[ModulePrincipalID(solutionID)]
	if !bound {
		return nil, nil, fmt.Errorf("%w: %w: solution %q has no module principal entry, so no namespace is bound to it",
			ErrSolutionAuditDeclarationRejected, ErrSolutionAuditNamespaceUnbound, solutionID)
	}
	namespaces := make([]string, 0, len(declared))
	for _, d := range declared {
		if len(namespaces) == 0 || namespaces[len(namespaces)-1] != d.Namespace {
			namespaces = append(namespaces, d.Namespace)
		}
	}
	// Every rule that needs no read is checked before any lock is taken.
	for _, namespace := range namespaces {
		// A namespace the composed event catalog publishes domain events under
		// has its producer already, though no audit_event_types row may name it.
		if eventcatalog.IsPublishedNamespace(namespace) {
			return nil, nil, fmt.Errorf("%w: %w: namespace %q is published by a producer in the composed event catalog",
				ErrSolutionAuditDeclarationRejected, ErrSolutionAuditNamespaceOwned, namespace)
		}
		if !grant.allowsNamespace(namespace) {
			return nil, nil, fmt.Errorf("%w: %w: namespace %q is not bound to solution %q",
				ErrSolutionAuditDeclarationRejected, ErrSolutionAuditNamespaceUnbound, namespace, solutionID)
		}
	}
	// The second key. A producer declaring a type external states that the fact
	// is customer-facing; only the operator can say the producer may send
	// anything out of the platform at all, and it says so per namespace on the
	// same entry that binds it. Refused here rather than dropped at delivery,
	// so a composition that forgot the grant fails the registration that
	// declared it instead of going quiet in production.
	for _, d := range declared {
		if !d.ExternallyDeliverable() || grant.allowsExternalNamespace(d.Namespace) {
			continue
		}
		return nil, nil, fmt.Errorf(
			"%w: %w: event type %q is declared with %q visibility but namespace %q is not among the external namespaces the operator granted solution %q",
			ErrSolutionAuditDeclarationRejected, ErrSolutionAuditNamespaceNotExternal,
			d.Type, AuditVisibilityExternal, d.Namespace, solutionID)
	}
	// declared is sorted by type, and '.' sorts below every character a
	// namespace may contain, so namespaces is sorted and each appears once;
	// taking the locks in that order is what keeps two admissions spanning the
	// same namespaces from deadlocking.
	var (
		takenOver []string
		written   []EventType
	)
	for _, namespace := range namespaces {
		if err := s.store.LockAuditEventNamespace(ctx, namespace); err != nil {
			return nil, nil, err
		}
		owners, err := s.store.ListAuditEventNamespaceOwners(ctx, namespace)
		if err != nil {
			return nil, nil, err
		}
		for _, existing := range owners {
			if existing == owner {
				continue
			}
			if !s.releasedAuditNamespace(existing, namespace) {
				return nil, nil, fmt.Errorf("%w: %w: namespace %q is held by %q", ErrSolutionAuditDeclarationRejected,
					ErrSolutionAuditNamespaceOwned, namespace, existing)
			}
			if err := s.store.TransferAuditEventNamespace(ctx, namespace, existing, owner); err != nil {
				return nil, nil, err
			}
			takenOver = append(takenOver, namespace)
		}
	}
	for _, d := range declared {
		admitted, err := s.store.GetDeclaredAuditEventType(ctx, d.Type)
		if err != nil {
			return nil, nil, err
		}
		if admitted != nil {
			// Visibility is what decides whether this type's events leave the
			// platform, and both directions of a change are silent to everyone
			// affected: narrowing stops deliveries to endpoints already
			// subscribed, widening starts sending facts out under a name a
			// tenant subscribed to when it meant something else.
			if admitted.Visibility != d.Visibility {
				return nil, nil, declarationRejected(
					"event type %q is admitted with %q visibility and may not become %q; declare another type instead",
					d.Type, admitted.Visibility, d.Visibility)
			}
			if err := checkAdditiveAuditFieldChange(*admitted, d); err != nil {
				return nil, nil, err
			}
			if sameDeclaredAuditEventType(*admitted, d) {
				continue
			}
		}
		if err := s.store.PutDeclaredAuditEventType(ctx, d); err != nil {
			return nil, nil, err
		}
		written = append(written, d.Type)
	}
	return takenOver, written, nil
}

// releasedAuditNamespace reports whether the operator has released a namespace
// from the solution holding it: the holder is a solution, and its principal
// entry — if it still has one — no longer binds the namespace. A code-owned
// holder is never released this way.
func (s *Service) releasedAuditNamespace(holder, namespace string) bool {
	solutionID, ok := SolutionIDFromAuditOwner(holder)
	if !ok {
		return false
	}
	grant, bound := s.modulePrincipals[ModulePrincipalID(solutionID)]
	return !bound || !grant.allowsNamespace(namespace)
}

// AuditEventTypes is the whole registry a reader can name: the code-owned
// catalog and every type a registered solution declared, sorted by type. A
// declared type is labelled by its owner, `solution:<id>`, and the `solution`
// category. Types are registered per deployment, as solutions are, so every
// authenticated reader sees the same set.
func (s *Service) AuditEventTypes(ctx context.Context) ([]AuditEventDefinition, error) {
	definitions := AuditEventCatalog()
	var declared []DeclaredAuditEventType
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		declared, err = s.store.ListDeclaredAuditEventTypes(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("list declared audit event types: %w", err)
	}
	for _, d := range declared {
		definitions = append(definitions, d.Definition())
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Type < definitions[j].Type })
	return definitions, nil
}

// lookupDeclaredAuditEventType reads one declared type for the emission path.
func (s *Service) lookupDeclaredAuditEventType(ctx context.Context, eventType EventType) (*DeclaredAuditEventType, error) {
	var declared *DeclaredAuditEventType
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		declared, err = s.store.GetDeclaredAuditEventType(ctx, eventType)
		return err
	})
	return declared, err
}
