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
//   - A namespace belongs to one producer. The first solution to admit a type
//     into it owns it; another solution, or the code catalog, holding any type
//     there refuses the declaration, and so does a namespace the composed event
//     catalog publishes domain events under (eventcatalog.IsPublishedNamespace).
//     This is the naming law domain events already follow (EVENTS.md): two
//     producers can never mint the same name.
//   - A type is declared at most once in one declaration.
//   - Field names are snake_case, unique within the type, and never `solution`,
//     which the host stamps on every emitted payload.
//   - A field kind is one of the registry's kinds, `number` (finite) included;
//     `enum` names its values and nothing else does.
//   - Re-registering an identical declaration changes nothing. A changed field
//     set is additive only: a field may be added, and an enum may gain values,
//     but no field is removed, retyped, or loses a value — rows already written
//     under the earlier set must still mean what they meant.
//   - A type is never removed. A declaration that stops listing it leaves it
//     admitted and owned, because historical rows keep referencing it.

// SolutionAuditOwnerPrefix prefixes the owner of every solution-declared audit
// event type, followed by the solution id — the same subject form the
// registration credential proves (`sub=solution:<id>`). The code-owned catalog
// never uses it, which is how the startup projection sync tells the two apart.
const SolutionAuditOwnerPrefix = "solution:"

// DeclaredAuditEventVersion is the version every solution-declared type
// carries. A field set only ever grows, so no change requires a new version.
const DeclaredAuditEventVersion = 1

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
	Fields      []PayloadField
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
		Fields:      append([]PayloadField(nil), d.Fields...),
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
		Items  *struct {
			Type string `json:"type"`
		} `json:"items"`
	} `json:"properties"`
}

// DeclaredAuditEventTypeFromSchema reads a declared type back from its stored
// row. It inverts PayloadSchemaJSON exactly and refuses anything else, so a row
// that was not written by admission never validates a payload.
func DeclaredAuditEventTypeFromSchema(eventType EventType, namespace, owner string, schema []byte) (DeclaredAuditEventType, error) {
	solutionID, ok := SolutionIDFromAuditOwner(owner)
	if !ok {
		return DeclaredAuditEventType{}, fmt.Errorf("audit: event type %q is not owned by a solution", eventType)
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
	}
	for name, property := range stored.Properties {
		if name == hostStampedAuditField {
			continue
		}
		field := PayloadField{Name: name}
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
	Fields      json.RawMessage `json:"fields"`
}

type declaredFieldJSON struct {
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
	Values *[]string `json:"values"`
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
	var (
		declared []DeclaredAuditEventType
		seen     = map[EventType]bool{}
	)
	for _, raw := range document.Dashboard.Events {
		var event declaredEventJSON
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, declarationRejected("a dashboard event cannot be read: %v", err)
		}
		if event.Fields == nil {
			// Binds an existing type; nothing to admit.
			continue
		}
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
		fields, err := parseDeclaredAuditFields(event.Type, event.Fields)
		if err != nil {
			return nil, err
		}
		declared = append(declared, DeclaredAuditEventType{
			Type:        eventType,
			Namespace:   namespace,
			SolutionID:  solutionID,
			Description: strings.TrimSpace(event.Description),
			Fields:      fields,
		})
	}
	if len(declared) > maxDeclaredAuditEventTypes {
		return nil, declarationRejected("%d event types are declared; at most %d may be", len(declared), maxDeclaredAuditEventTypes)
	}
	sort.Slice(declared, func(i, j int) bool { return declared[i].Type < declared[j].Type })
	return declared, nil
}

func parseDeclaredAuditFields(eventType string, raw json.RawMessage) ([]PayloadField, error) {
	var entries []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &entries) != nil {
		return nil, declarationRejected("event type %q fields must be an array", eventType)
	}
	if len(entries) > maxDeclaredAuditEventFields {
		return nil, declarationRejected("event type %q declares %d fields; at most %d may be", eventType, len(entries), maxDeclaredAuditEventFields)
	}
	fields := make([]PayloadField, 0, len(entries))
	names := map[string]bool{}
	for _, entry := range entries {
		decoder := json.NewDecoder(bytes.NewReader(entry))
		decoder.DisallowUnknownFields()
		var field declaredFieldJSON
		if err := decoder.Decode(&field); err != nil {
			return nil, declarationRejected("event type %q has an unreadable field: %v", eventType, err)
		}
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
		payloadField := PayloadField{Name: field.Name, Kind: kind}
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

func declaredEnumValues(eventType string, field declaredFieldJSON) ([]string, error) {
	if field.Values == nil || len(*field.Values) == 0 {
		return nil, declarationRejected("event type %q enum field %q must declare its values", eventType, field.Name)
	}
	if len(*field.Values) > maxDeclaredAuditEventEnumItems {
		return nil, declarationRejected("event type %q enum field %q declares more than %d values", eventType, field.Name, maxDeclaredAuditEventEnumItems)
	}
	seen := map[string]bool{}
	values := make([]string, 0, len(*field.Values))
	for _, value := range *field.Values {
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

// admitDeclaredAuditEventTypes records a solution's declared types. It must run
// inside the registration write's control-plane transaction: the namespace
// locks it takes hold until that transaction ends, and a refusal here rolls the
// registration write back with it.
func (s *Service) admitDeclaredAuditEventTypes(ctx context.Context, solutionID string, declared []DeclaredAuditEventType) error {
	if len(declared) == 0 {
		return nil
	}
	owner := SolutionAuditOwner(solutionID)
	namespaces := make([]string, 0, len(declared))
	for _, d := range declared {
		if len(namespaces) == 0 || namespaces[len(namespaces)-1] != d.Namespace {
			namespaces = append(namespaces, d.Namespace)
		}
	}
	// declared is sorted by type, so namespaces is sorted and each appears once;
	// taking the locks in that order is what keeps two admissions spanning the
	// same namespaces from deadlocking.
	for _, namespace := range namespaces {
		// A namespace the composed event catalog publishes domain events under
		// has its producer already, though no audit_event_types row may name it.
		if eventcatalog.IsPublishedNamespace(namespace) {
			return fmt.Errorf("%w: %w: namespace %q is published by a producer in the composed event catalog",
				ErrSolutionAuditDeclarationRejected, ErrSolutionAuditNamespaceOwned, namespace)
		}
		if err := s.store.LockAuditEventNamespace(ctx, namespace); err != nil {
			return err
		}
		owners, err := s.store.ListAuditEventNamespaceOwners(ctx, namespace)
		if err != nil {
			return err
		}
		for _, existing := range owners {
			if existing != owner {
				return fmt.Errorf("%w: %w: namespace %q is held by %q", ErrSolutionAuditDeclarationRejected,
					ErrSolutionAuditNamespaceOwned, namespace, existing)
			}
		}
	}
	for _, d := range declared {
		admitted, err := s.store.GetDeclaredAuditEventType(ctx, d.Type)
		if err != nil {
			return err
		}
		if admitted != nil {
			if err := checkAdditiveAuditFieldChange(*admitted, d); err != nil {
				return err
			}
			if sameDeclaredAuditEventType(*admitted, d) {
				continue
			}
		}
		if err := s.store.PutDeclaredAuditEventType(ctx, d); err != nil {
			return err
		}
	}
	return nil
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
