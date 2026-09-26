package connector_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"accounts/pkg/datasource/connector"
)

type stubFiles struct{ d connector.Descriptor }

func (s stubFiles) Descriptor() connector.Descriptor { return s.d }
func (s stubFiles) Version(context.Context, connector.Source) (string, error) {
	return "", nil
}
func (s stubFiles) Changes(context.Context, connector.Source, string) (connector.ChangeSet, error) {
	return connector.ChangeSet{}, nil
}
func (s stubFiles) FetchFiles(context.Context, connector.Source, string, []connector.FileRef, func(connector.File, io.Reader) error) error {
	return nil
}

type stubRecords struct{ stubFiles }

func good(key string) connector.Descriptor {
	return connector.Descriptor{
		Key: key, DisplayName: "Example", Description: "An example provider.",
		Interface:       connector.InterfaceFiles,
		CredentialModes: []connector.CredentialMode{connector.CredentialOrgApp},
		Readers:         connector.ReadersSourceScoped,
		Budget:          connector.Budget{MaxItemsPerCall: 10, MaxBytesPerCall: 100, MaxItemBytes: 50},
		Conformant:      true,
	}
}

func TestRegistryRefusesBadRegistrationsAtStart(t *testing.T) {
	mutate := func(f func(*connector.Descriptor)) connector.Descriptor { d := good("example"); f(&d); return d }
	bad := map[string]connector.Descriptor{
		"bad key":             mutate(func(d *connector.Descriptor) { d.Key = "Example Drive" }),
		"no interface":        mutate(func(d *connector.Descriptor) { d.Interface = "blobs" }),
		"no credential mode":  mutate(func(d *connector.Descriptor) { d.CredentialModes = nil }),
		"unknown credential":  mutate(func(d *connector.Descriptor) { d.CredentialModes = []connector.CredentialMode{"password"} }),
		"no readers model":    mutate(func(d *connector.Descriptor) { d.Readers = "" }),
		"no budget":           mutate(func(d *connector.Descriptor) { d.Budget = connector.Budget{} }),
		"item over batch":     mutate(func(d *connector.Descriptor) { d.Budget.MaxItemBytes = 1000 }),
		"conformant with gap": mutate(func(d *connector.Descriptor) { d.Gap = "#1" }),
		"duplicate config key": mutate(func(d *connector.Descriptor) {
			d.ConfigFields = []connector.ConfigField{{Key: "a", DisplayName: "A"}, {Key: "a", DisplayName: "A"}}
		}),
	}
	for name, d := range bad {
		if err := connector.NewRegistry().Register(stubFiles{d}); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	records := good("crm")
	records.Interface = connector.InterfaceRecords
	if err := connector.NewRegistry().Register(stubRecords{stubFiles{records}}); err == nil {
		t.Error("a records connector registered with no records bulk fetch in the host")
	}
	r := connector.NewRegistry()
	if err := r.Register(stubFiles{good("example")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(stubFiles{good("example")}); err == nil {
		t.Error("a key registered twice")
	}
	nc := good("legacy")
	nc.Conformant = false
	if err := r.RegisterNonConformant(nc); err == nil {
		t.Error("a non-conformant provider registered without naming its gap")
	}
	if err := r.Register(stubFiles{nc}); err == nil {
		t.Error("Register took a non-conformant connector")
	}
}

func TestAdmissionFollowsTheSettledRules(t *testing.T) {
	legacy := good("legacy")
	legacy.Conformant, legacy.Gap = false, "no cursor and no deletions (tracked)"
	translated := good("drive")
	translated.Readers = connector.ReadersTranslated

	r := connector.NewRegistry()
	for _, err := range []error{r.Register(stubFiles{good("example")}), r.RegisterNonConformant(legacy), r.Register(stubFiles{translated})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := r.AdmitNewSource("example"); err != nil {
		t.Fatalf("a conformant source-scoped connector is admitted: %v", err)
	}
	if err := r.AdmitNewSource("legacy"); !errors.Is(err, connector.ErrNonConformant) {
		t.Fatalf("a non-conformant provider takes no new source: %v", err)
	}
	if err := r.AdmitNewSource("drive"); !errors.Is(err, connector.ErrReadersNotEnforced) {
		t.Fatalf("a translating connector waits for read-side enforcement: %v", err)
	}
	if err := r.AdmitNewSource("nope"); !errors.Is(err, connector.ErrUnknownConnector) {
		t.Fatalf("unknown key: %v", err)
	}
	if _, ok := r.Connector("legacy"); ok {
		t.Fatal("a non-conformant provider has no connector")
	}
	if got := len(r.Descriptors()); got != 3 {
		t.Fatalf("descriptors = %d, want 3", got)
	}
	if got := len(r.Conformant()); got != 2 {
		t.Fatalf("conformant = %d, want 2", got)
	}

	enforced := connector.NewRegistry(connector.WithReadersEnforcedAtRead())
	if err := enforced.Register(stubFiles{translated}); err != nil {
		t.Fatal(err)
	}
	if err := enforced.AdmitNewSource("drive"); err != nil {
		t.Fatalf("with read-side enforcement a translating connector is admitted: %v", err)
	}
}

func TestInterfacesDeclareTheirItemShapes(t *testing.T) {
	want := map[connector.Interface][]connector.ItemShape{
		connector.InterfaceFiles:    {connector.ShapeContent},
		connector.InterfacePages:    {connector.ShapeContent},
		connector.InterfaceRecords:  {connector.ShapeStructured},
		connector.InterfaceEvents:   {connector.ShapeStructured},
		connector.InterfaceMessages: {connector.ShapeContent, connector.ShapeStructured},
	}
	for i, shapes := range want {
		got := i.Shapes()
		if len(got) != len(shapes) {
			t.Fatalf("%s items are %v, want %v", i, got, shapes)
		}
		for k := range shapes {
			if got[k] != shapes[k] {
				t.Fatalf("%s items are %v, want %v", i, got, shapes)
			}
		}
	}
	if connector.Interface("blobs").Shapes() != nil {
		t.Fatal("an unknown interface has no shape")
	}
}

func TestChangeSetKeyIsStable(t *testing.T) {
	a := connector.ChangeSet{SourceID: "s", From: "1", To: "2"}
	b := connector.ChangeSet{SourceID: "s", From: "1", To: "2", Changes: []connector.Change{{Kind: connector.ChangeAdded}}}
	c := connector.ChangeSet{SourceID: "s", From: "12", To: ""}
	if a.IdempotencyKey() != b.IdempotencyKey() || a.IdempotencyKey() == c.IdempotencyKey() {
		t.Fatal("a change set's key is its source, from and to, unambiguously")
	}
}
