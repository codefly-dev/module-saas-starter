package connector

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// CredentialMode is how a connector authenticates to its provider. The host
// holds every credential; the mode only says which kind the connect flow asks
// for.
type CredentialMode string

const (
	// CredentialNone: the provider serves the source without a credential.
	CredentialNone CredentialMode = "none"
	// CredentialOrgApp: an org-level app or installation the host holds.
	CredentialOrgApp CredentialMode = "org_app"
	// CredentialUserOAuth: a person's own OAuth grant; the source is personal.
	CredentialUserOAuth CredentialMode = "user_oauth"
	// CredentialStaticSecret: a token or key the tenant pastes once.
	CredentialStaticSecret CredentialMode = "static_secret"
)

// ReadersModel is how a connector derives an item's readers.
type ReadersModel string

const (
	// ReadersSourceScoped: the provider has no per-item access list; every item
	// is readable by whoever may read the source's boundary.
	ReadersSourceScoped ReadersModel = "source_scoped"
	// ReadersTranslated: the provider has per-item access lists, translated
	// through Translate.
	ReadersTranslated ReadersModel = "translated"
)

// ConfigField is one non-secret configuration input a connector needs, so a
// client renders the connect form from the catalog.
type ConfigField struct {
	Key         string
	DisplayName string
	Help        string
	Required    bool
}

// Budget is clause 6: the limits a connector serves within. A bulk fetch
// naming more than MaxItemsPerCall items, or more than MaxBytesPerCall bytes,
// is refused as ErrBatchTooLarge; an item past MaxItemBytes as ErrItemTooLarge.
//
// OperationsPerWindow is the quota one provider credential may spend per
// Window, shared by every source and every replica that uses it; a person's
// sync may spend all of it, a background sync only BackgroundSharePercent of
// it, so background work always leaves room for a person (budget.go).
type Budget struct {
	MaxItemsPerCall        int
	MaxBytesPerCall        int64
	MaxItemBytes           int64
	OperationsPerWindow    int
	Window                 time.Duration
	BackgroundSharePercent int
}

// Descriptor is one catalog entry: everything a client needs to offer and
// configure a connector, and everything the host needs to admit a source of
// it. It is the catalog GetDatasourceCatalog serves.
type Descriptor struct {
	Key             string
	DisplayName     string
	Description     string
	Interface       Interface
	ConfigFields    []ConfigField
	CredentialModes []CredentialMode
	SupportsWebhook bool
	Readers         ReadersModel
	Budget          Budget
	// Conformant is true only for a connector that implements its interface
	// and passes the conformance suite. A non-conformant provider keeps its
	// existing sources running, flagged, and cannot be newly connected.
	Conformant bool
	// Gap names what keeps a non-conformant provider off the envelope and
	// where it is tracked. Required when Conformant is false, empty otherwise.
	Gap string
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Validate reports why a descriptor cannot be registered, or nil.
func (d Descriptor) Validate() error {
	var problems []string
	if !keyPattern.MatchString(d.Key) {
		problems = append(problems, fmt.Sprintf("key %q must match %s", d.Key, keyPattern))
	}
	if d.DisplayName == "" || d.Description == "" {
		problems = append(problems, "display name and description are required")
	}
	if !d.Interface.valid() {
		problems = append(problems, fmt.Sprintf("interface %q is not one of files, pages, records, messages, events", d.Interface))
	}
	if len(d.CredentialModes) == 0 {
		problems = append(problems, "at least one credential mode is required")
	}
	for _, m := range d.CredentialModes {
		switch m {
		case CredentialNone, CredentialOrgApp, CredentialUserOAuth, CredentialStaticSecret:
		default:
			problems = append(problems, fmt.Sprintf("credential mode %q is unknown", m))
		}
	}
	seen := map[string]bool{}
	for _, f := range d.ConfigFields {
		if f.Key == "" || f.DisplayName == "" || seen[f.Key] {
			problems = append(problems, fmt.Sprintf("config field %q needs a distinct key and a display name", f.Key))
		}
		seen[f.Key] = true
	}
	if d.Conformant {
		if d.Gap != "" {
			problems = append(problems, "a conformant connector names no gap")
		}
		if d.Readers != ReadersSourceScoped && d.Readers != ReadersTranslated {
			problems = append(problems, fmt.Sprintf("readers model %q is unknown", d.Readers))
		}
		if d.Budget.MaxItemsPerCall <= 0 || d.Budget.MaxBytesPerCall <= 0 || d.Budget.MaxItemBytes <= 0 ||
			d.Budget.MaxItemBytes > d.Budget.MaxBytesPerCall {
			problems = append(problems, "budget must declare positive per-call item and byte limits, and an item limit within the byte limit")
		}
		if d.Budget.OperationsPerWindow <= 0 || d.Budget.Window <= 0 ||
			d.Budget.BackgroundSharePercent <= 0 || d.Budget.BackgroundSharePercent >= 100 {
			problems = append(problems, "budget must declare a quota of operations per window and a background share between 1 and 99 percent")
		}
	} else if d.Gap == "" {
		problems = append(problems, "a non-conformant provider must name its tracked gap")
	}
	if len(problems) > 0 {
		return fmt.Errorf("connector descriptor %q: %v", d.Key, problems)
	}
	return nil
}

// Admission refusals: why a new source of a connector cannot be connected.
var (
	ErrUnknownConnector = errors.New("connector: no connector is registered under that key")
	// ErrNonConformant: settled question 7 — a provider off the envelope keeps
	// its existing sources but takes no new ones until it passes the suite.
	ErrNonConformant = errors.New("connector: this provider does not meet the datasource envelope yet, so new sources of it are refused; existing sources keep running")
	// ErrReadersNotEnforced: a TRANSLATED connector is refused until the
	// stores enforce per-item readers at read, so nothing over-shares first.
	ErrReadersNotEnforced = errors.New("connector: per-item readers are not yet enforced at read, so a connector that translates provider permissions cannot be connected")
)

// Registry is the host's descriptor-driven connector registry. It is built
// once at start; a bad registration is a start-up failure, never a runtime one.
type Registry struct {
	mu                    sync.RWMutex
	entries               map[string]entry
	readersEnforcedAtRead bool
}

type entry struct {
	descriptor Descriptor
	connector  Connector
}

// Option configures a Registry.
type Option func(*Registry)

// WithReadersEnforcedAtRead declares that the stores enforce per-item readers
// at read, which is what admits a TRANSLATED connector. Only set it when that
// is true of the deployment.
func WithReadersEnforcedAtRead() Option {
	return func(r *Registry) { r.readersEnforcedAtRead = true }
}

// NewRegistry returns an empty registry.
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{entries: map[string]entry{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a conformant connector. Its descriptor must validate, declare
// itself conformant, and name an interface the connector actually implements.
func (r *Registry) Register(c Connector) error {
	d := c.Descriptor()
	if !d.Conformant {
		return fmt.Errorf("connector %q: Register takes conformant connectors; use RegisterNonConformant", d.Key)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	switch d.Interface {
	case InterfaceFiles:
		if _, ok := c.(FilesConnector); !ok {
			return fmt.Errorf("connector %q declares the files interface but does not implement FilesConnector", d.Key)
		}
	default:
		return fmt.Errorf("connector %q: the %s interface has no bulk fetch in this host yet, so no connector of it can be conformant", d.Key, d.Interface)
	}
	return r.add(d, c)
}

// RegisterNonConformant adds a provider that still runs on its own engine,
// outside the envelope. Its descriptor must say so and name its gap.
func (r *Registry) RegisterNonConformant(d Descriptor) error {
	if d.Conformant {
		return fmt.Errorf("connector %q: a non-conformant registration cannot declare itself conformant", d.Key)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	return r.add(d, nil)
}

func (r *Registry) add(d Descriptor, c Connector) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[d.Key]; dup {
		return fmt.Errorf("connector %q is already registered", d.Key)
	}
	r.entries[d.Key] = entry{descriptor: d, connector: c}
	return nil
}

// Descriptor returns the registered descriptor under key.
func (r *Registry) Descriptor(key string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	return e.descriptor, ok
}

// Connector returns the conformant connector under key; a non-conformant or
// unknown key has none.
func (r *Registry) Connector(key string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	if !ok || e.connector == nil {
		return nil, false
	}
	return e.connector, true
}

// Files returns the conformant files connector under key.
func (r *Registry) Files(key string) (FilesConnector, bool) {
	c, ok := r.Connector(key)
	if !ok {
		return nil, false
	}
	f, ok := c.(FilesConnector)
	return f, ok
}

// Descriptors returns every registered descriptor, ordered by key.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Conformant returns every conformant connector, ordered by key: the set the
// conformance suite must cover.
func (r *Registry) Conformant() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Connector
	for _, e := range r.entries {
		if e.connector != nil {
			out = append(out, e.connector)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Descriptor().Key < out[j].Descriptor().Key })
	return out
}

// AdmitNewSource reports whether a new source of the connector under key may be
// connected: it must be registered, conformant, and — if it translates provider
// permissions — the stores must enforce per-item readers at read.
func (r *Registry) AdmitNewSource(key string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	switch {
	case !ok:
		return ErrUnknownConnector
	case !e.descriptor.Conformant:
		return ErrNonConformant
	case e.descriptor.Readers == ReadersTranslated && !r.readersEnforcedAtRead:
		return ErrReadersNotEnforced
	}
	return nil
}
