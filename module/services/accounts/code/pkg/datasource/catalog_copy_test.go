package datasource_test

import (
	"regexp"
	"testing"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"
	"accounts/pkg/datasource/objectstore"
)

// What a connector's catalog entry says is read by an organization
// administrator connecting a source, word for word: the description under the
// provider and the help under each field. It names what the person types and
// why, never how the host does it — no syntax engine, no internal service, no
// protocol term a person must look up.
var hostVocabulary = regexp.MustCompile(`(?i)\bRE2\b|object-storage service|\bservice\b|\borigin\b|key after the prefix|\bconnector\b|\benvelope\b|\bwebhooks?\b|\bpulled\b`)

func TestConnectableProvidersDescribeThemselvesInPlainWords(t *testing.T) {
	for _, d := range []connector.Descriptor{
		github.NewFilesConnector(nil).Descriptor(),
		objectstore.NewFilesConnector(nil, nil).Descriptor(),
	} {
		if word := hostVocabulary.FindString(d.Description); word != "" {
			t.Errorf("%s description says %q: %q", d.Key, word, d.Description)
		}
		for _, f := range d.ConfigFields {
			if word := hostVocabulary.FindString(f.Help); word != "" {
				t.Errorf("%s field %s help says %q: %q", d.Key, f.Key, word, f.Help)
			}
		}
	}
}
