package business

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The host's datasource connectors, on the envelope in pkg/datasource/connector.
// GitHub is the one conformant connector: it passes the conformance suite
// (pkg/datasource/github/connector_conformance_test.go). The generic API, the
// crawler and object storage still run on their own engines, outside the
// envelope; they are registered non-conformant with their gap, so their
// existing sources keep running and new ones are refused until each passes the
// suite (the owner's settled question 7).

// Why each legacy provider is not on the envelope yet. Every one of them
// re-sends everything as an addition on every sync, with no cursor, and never
// reports a deletion, so content removed at the source is found again forever.
const (
	gapAPIDatasource     = "one untyped response with no item identity, no cursor and no deletions; to be rebuilt as a records connector or retired"
	gapCrawlerDatasource = "no cursor and no deletions (a page leaving the sitemap is never removed), and one fetch per page"
	gapUploadDatasource  = "no cursor and no deletions (a removed object is never removed), and one fetch per object"
)

// newDatasourceConnectorRegistry builds the host's registry. A registration
// that does not validate is a programming error in this file, so it fails the
// host at start rather than at a tenant's first connect.
func (s *Service) newDatasourceConnectorRegistry() *connector.Registry {
	r := connector.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(fmt.Sprintf("datasource connector registry: %v", err))
		}
	}
	must(r.Register(github.NewFilesConnector(s.githubRemoteForSource)))
	must(r.RegisterNonConformant(connector.Descriptor{
		Key: DatasourceProviderAPI, DisplayName: "HTTP API",
		Description:     "An HTTP API with a stored credential; a configured resource is fetched on sync.",
		Interface:       connector.InterfaceRecords,
		CredentialModes: []connector.CredentialMode{connector.CredentialStaticSecret},
		Gap:             gapAPIDatasource,
	}))
	must(r.RegisterNonConformant(connector.Descriptor{
		Key: DatasourceProviderCrawler, DisplayName: "Web crawler",
		Description:     "A documentation website, ingested from its sitemap.xml on sync. Needs no credential.",
		Interface:       connector.InterfacePages,
		CredentialModes: []connector.CredentialMode{connector.CredentialNone},
		Gap:             gapCrawlerDatasource,
	}))
	must(r.RegisterNonConformant(connector.Descriptor{
		Key: DatasourceProviderUpload, DisplayName: "Object storage",
		Description:     "An S3-compatible bucket; objects under a prefix are pulled on sync.",
		Interface:       connector.InterfaceFiles,
		CredentialModes: []connector.CredentialMode{connector.CredentialStaticSecret},
		Gap:             gapUploadDatasource,
	}))
	return r
}

// DatasourceConnectors is the host's connector registry; nil until the
// datasource connector is configured.
func (s *Service) DatasourceConnectors() *connector.Registry { return s.datasourceConnectors }

// DatasourceCatalogEntry is one registered connector as the catalog serves it:
// its descriptor, and whether a new source of it may be connected now.
type DatasourceCatalogEntry struct {
	Descriptor        connector.Descriptor
	AcceptsNewSources bool
}

// DatasourceCatalog is the host's whole connector registry, ordered by key:
// every provider, conformant or not, with whether it accepts a new source. A
// client offers the ones that do and flags the ones that do not.
func (s *Service) DatasourceCatalog() []DatasourceCatalogEntry {
	if s.datasourceConnectors == nil {
		return nil
	}
	var out []DatasourceCatalogEntry
	for _, d := range s.datasourceConnectors.Descriptors() {
		out = append(out, DatasourceCatalogEntry{Descriptor: d, AcceptsNewSources: s.datasourceConnectors.AdmitNewSource(d.Key) == nil})
	}
	return out
}

// DatasourceConformance reports whether a provider meets the connector
// envelope and, when it does not, why. An unregistered provider is not
// conformant, and says so.
func DatasourceConformance(registry *connector.Registry, provider string) (bool, string) {
	if registry == nil {
		return false, "the datasource connector is not configured"
	}
	d, ok := registry.Descriptor(provider)
	if !ok {
		return false, "no connector is registered for this provider"
	}
	return d.Conformant, d.Gap
}

// admitNewDatasource refuses a new source of a provider the registry does not
// admit, before anything is validated, sealed or stored.
func (s *Service) admitNewDatasource(provider string) error {
	if s.datasourceConnectors == nil {
		return status.Error(codes.FailedPrecondition, "datasource connector is not configured")
	}
	err := s.datasourceConnectors.AdmitNewSource(provider)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, connector.ErrUnknownConnector):
		return status.Errorf(codes.InvalidArgument, "unknown datasource provider %q", provider)
	default:
		return status.Error(codes.FailedPrecondition, err.Error())
	}
}

// connectorSource is the envelope's view of a stored source: its identity and
// tenancy, and — for GitHub — its repository, branch and the host's own scope.
func connectorSource(source *DatasourceSource) connector.Source {
	src := connector.Source{ID: source.ID, OrgID: source.OrgID, BoundaryNodeID: source.BoundaryNodeID}
	if source.Provider == DatasourceProviderGitHub {
		src.Config = github.SourceConfig{Repo: source.Repo, Branch: source.Branch, InScope: datasourceFileScope(source)}
	}
	return src
}

// githubRemoteForSource resolves the credentialed GitHub client for one source,
// per call: the host holds the credential and the connector never sees it.
func (s *Service) githubRemoteForSource(ctx context.Context, src connector.Source) (github.Remote, error) {
	source, err := s.store.GetDatasourceSourceByID(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if source == nil || source.OrgID != src.OrgID || source.Provider != DatasourceProviderGitHub {
		return nil, fmt.Errorf("datasource source %s is not a GitHub source of org %s", src.ID, src.OrgID)
	}
	client, err := s.githubClientForSource(ctx, source)
	if err != nil {
		return nil, err
	}
	return githubRemote{client}, nil
}

// githubRemote adapts the host's per-source client to the connector's Remote.
type githubRemote struct{ GitHubContentClient }

func (r githubRemote) OpenMirror(ctx context.Context, ws github.Workspace, repo string) (github.Mirror, error) {
	m, err := r.OpenRepository(ctx, ws, repo)
	if err != nil {
		return nil, err
	}
	return m, nil
}
