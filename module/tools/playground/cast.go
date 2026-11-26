package playground

// FixturePath is the fixture the playground host is started with, from this
// package's directory.
const FixturePath = "../../fixtures/dev-admin.yaml"

// Home is the organization the journeys run in; Elsewhere is one none of
// their people belongs to.
const (
	Home      = "Acme Corp"
	Elsewhere = "Globex"
)

// SignIn is the three page-role cases, by the id it offers
// them under: Dana, Carol and Bob. In this fixture each is a member of Home
// holding no grant on the example collection, so each reads nothing of it.
var SignIn = []string{"dev-dana", "dev-carol", "dev-bob"}

// Supporting is the fixture people the journeys still need beside the three,
// because none of the three administers Home or holds a grant in it:
//
//   - Owner owns Home and the example collection, and holds super_admin: the
//     grants are hers to give, and she is the platform role with nothing for
//     free;
//   - Reader reads the collection by an explicit grant the fixture seeds;
//   - Grantee is granted and revoked during the run, so is nobody whose
//     holdings the fixture fixes;
//   - Administrators administer Home and hold no grant.
//
// The sign-in picker also offers the owner for platform administration.
var Supporting = supporting{
	Owner:          "dev-admin",
	Reader:         "fixture-maya",
	Grantee:        "fixture-remi",
	Administrators: []string{"dev-alice", "fixture-alex"},
}

type supporting struct {
	Owner, Reader, Grantee string
	Administrators         []string
}

func (s supporting) all() []string {
	return append([]string{s.Owner, s.Reader, s.Grantee}, s.Administrators...)
}
