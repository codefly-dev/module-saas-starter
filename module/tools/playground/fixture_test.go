package playground

import "testing"

// The journeys sign in as the fixture's people by the ids below. A fixture
// that stops seeding one of them fails here, without a host, rather than
// half-way through a run against one.
func TestTheFixtureSeedsEveryPersonTheJourneysSignInAs(t *testing.T) {
	fixture, err := Load(FixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(append([]string{}, SignIn...), Supporting.all()...) {
		person, err := fixture.Person(id)
		if err != nil {
			t.Error(err)
			continue
		}
		if person.ID == "" || person.Email == "" {
			t.Errorf("%s: the fixture pins no principal id or email", id)
		}
	}
	for _, name := range []string{Home, Elsewhere} {
		if _, err := fixture.Organization(name); err != nil {
			t.Error(err)
		}
	}
}

// The picker offers the three page-role cases and the platform administrator.
func TestTheJourneysSignInAsTheSignInPagesPeople(t *testing.T) {
	fixture, err := Load(FixturePath)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, id := range fixture.SignInChoices {
		offered[id] = true
	}
	if !offered[Supporting.Owner] || len(offered) != len(SignIn)+1 {
		t.Fatalf("the sign-in page offers %v, the journeys sign in as %v", fixture.SignInChoices, SignIn)
	}
	for _, id := range SignIn {
		if !offered[id] {
			t.Errorf("the journeys sign in as %s, whom the sign-in page does not offer", id)
		}
	}
}
