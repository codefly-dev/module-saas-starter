package fixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	codefly "github.com/codefly-dev/sdk-go"
	"gopkg.in/yaml.v3"
)

// WorkspaceGrantsDocument is the workspace configuration document a composing
// workspace adds to the selected fixture: the roles its people hold at the
// modules it composes, and who holds them. This module cannot name those
// roles (a module's permissions are the module's words, not this host's), so
// the workspace that composes both states them, as it states every other value
// it composes: configurations/<profile>/fixture-grants.yaml, read by the SDK.
const WorkspaceGrantsDocument = "fixture-grants"

// workspaceGrants is the part of a fixture a composing workspace may add: roles
// and their assignments, over the people and organisations the fixture seeds.
type workspaceGrants struct {
	Roles       []fixtureRole           `yaml:"roles"`
	Assignments []fixtureRoleAssignment `yaml:"assignments"`
}

// readWorkspaceGrants returns the composing workspace's grants, nil when it
// states none. A document that is present and does not read is refused: a
// workspace that wrote grants believes its people hold them.
func readWorkspaceGrants(read func(name string) (json.RawMessage, error)) (*workspaceGrants, error) {
	content, err := read(WorkspaceGrantsDocument)
	if errors.Is(err, codefly.ErrConfigurationDocumentMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the workspace's %s document: %w", WorkspaceGrantsDocument, err)
	}
	var grants workspaceGrants
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&grants); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("cannot parse the workspace's %s document: %w", WorkspaceGrantsDocument, err)
	}
	return &grants, nil
}

// withWorkspaceGrants adds the composing workspace's roles and assignments to
// the fixture and validates the whole as one fixture; the seed then refuses, by
// name, an assignment naming a person, organisation or role it does not seed.
func withWorkspaceGrants(f *fixtureFile, grants *workspaceGrants) error {
	if grants == nil {
		return nil
	}
	f.Roles = append(f.Roles, grants.Roles...)
	f.Assignments = append(f.Assignments, grants.Assignments...)
	if err := validateFixture(f); err != nil {
		return fmt.Errorf("the workspace's %s document: %w", WorkspaceGrantsDocument, err)
	}
	return nil
}
