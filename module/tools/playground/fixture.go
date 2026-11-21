// Package playground holds the host-only playground journeys
// (module/PLAYGROUND.md): what the people of the dev-admin fixture may read of
// the example collection, checked against a host Codefly is running.
//
// The journeys are in journeys_test.go, behind the `playground` build tag,
// because they need that running host. This file is what they read the
// fixture with, and it is tested without one.
package playground

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Fixture is the part of a fixture document the journeys read.
type Fixture struct {
	SignInChoices []string `yaml:"sign_in_choices"`
	Users         []struct {
		ID         string `yaml:"id"`
		Email      string `yaml:"email"`
		ProviderID string `yaml:"provider_id"`
	} `yaml:"users"`
	Organizations []struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	} `yaml:"organizations"`
}

// Person is one fixture person: the principal the host seeded and the token
// the sign-in page presents for them.
type Person struct{ ID, Email, ProviderID string }

// Load reads a fixture document.
func Load(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fixture := &Fixture{}
	if err := yaml.Unmarshal(raw, fixture); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return fixture, nil
}

// Person finds a fixture person by the id the sign-in page offers them under.
func (f *Fixture) Person(providerID string) (Person, error) {
	for _, user := range f.Users {
		if user.ProviderID == providerID {
			return Person{ID: user.ID, Email: user.Email, ProviderID: user.ProviderID}, nil
		}
	}
	return Person{}, fmt.Errorf("the fixture seeds no person signing in as %q", providerID)
}

// Organization finds a fixture organization's id by name.
func (f *Fixture) Organization(name string) (string, error) {
	for _, organization := range f.Organizations {
		if organization.Name == name {
			return organization.ID, nil
		}
	}
	return "", fmt.Errorf("the fixture seeds no organization named %q", name)
}
