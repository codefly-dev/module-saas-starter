package fixtures

import (
	"context"
	"testing"
)

func TestSeedRejectsUnsafeFixtureName(t *testing.T) {
	err := Seed(context.Background(), nil, "../outside")
	if err == nil {
		t.Fatal("Seed() accepted a path-traversing fixture name")
	}
}

func TestSelectedNameUsesCodeflySDKAcrossAvailableFixtures(t *testing.T) {
	t.Setenv("CODEFLY__FIXTURE", "dev-admin")
	name, err := SelectedName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "dev-admin" {
		t.Fatalf("SelectedName() = %q, want dev-admin", name)
	}
}

func TestFixtureNamePatternAcceptsProductFixtureNames(t *testing.T) {
	for _, name := range []string{"simple", "dev-admin", "codefly_local-1"} {
		if !fixtureNamePattern.MatchString(name) {
			t.Fatalf("fixtureNamePattern rejected %q", name)
		}
	}
}
