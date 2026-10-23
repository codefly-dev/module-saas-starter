package business

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"
	"accounts/pkg/datasource/objectstore"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The host's datasource connectors, on the envelope in pkg/datasource/connector.
// GitHub is the one conformant connector: it passes the conformance suite
// (pkg/datasource/github/connector_conformance_test.go). The generic API and
// the crawler still run on their own engines, outside the envelope; object
// storage syncs through its envelope connector (objectstore.FilesConnector over
// the object-storage service) but reads one object per call. All three are
// registered non-conformant with their gap, so their existing sources keep
// running and new ones are refused until each passes the suite (the owner's
// settled question 7).

// Why each legacy provider is not on the envelope yet, per connector. What
// remains for each, clause by clause, and what it already honours, is in
// ../datasource/connector/CONFORMANCE.md; a test holds that document to this
// registry, so a gap cannot be edited here and left unexplained there.
//
// These sentences are the tenant-facing half: the wire carries each on every
// source of its provider (Datasource.conformance_gap) and a client shows it to
// explain why the provider takes no new sources. They name what is missing, not
// what the connector happens to do — an inaccurate one tells a tenant their
// content is being lost when it is not.
const (
	gapAPIDatasource     = "one untyped response with no item identity, no cursor and no deletions; to be rebuilt as a records connector or retired"
	gapCrawlerDatasource = "no cursor and no deletions (a page leaving the sitemap is never removed), and one fetch per page"
	// Object storage DOES report deletions: runUploadSync closes a sync that saw
	// the whole scope with the complete listing of what it delivered, which the
	// store tombstones the rest against. This sentence once said the opposite
	// — "a removed object is never removed" — which told every tenant of an
	// object-storage source that their deletions were being ignored, when the
	// mirror listing is exactly the mechanism that applies them. The sync reads
	// through the object-storage connector, whose cursor exists; what remains is
	// the service's single-key read (clause 8), and the host's sync re-sending
	// what the cursor already says is unchanged.
	gapUploadDatasource = "every sync re-sends every object in scope, and the object-storage service reads one object per call; deletions ARE applied, through the complete listing that closes a whole-scope sync"
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
	must(r.Register(connector.Budgeted(github.NewFilesConnector(s.githubRemoteForSource), datasourceScheduler{s: s})))
	must(r.RegisterNonConformant(connector.Descriptor{
		Key: DatasourceProviderAPI, DisplayName: "HTTP API",
		Description:     "An HTTP API with a stored credential and declared operations called through the host.",
		Budget:          apiOperationBudget,
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
		Description:     "An S3-compatible bucket; the objects under a prefix that pass its key filter are pulled on sync. A source is one collection with one set of readers, so a bucket shared by several audiences is several sources.",
		Interface:       connector.InterfaceFiles,
		CredentialModes: []connector.CredentialMode{connector.CredentialStaticSecret},
		// A bucket has no per-object access list, so the catalog says before a
		// person asks that every object is readable by the collection's readers.
		Readers: connector.ReadersSourceScoped,
		Gap:     gapUploadDatasource,
	}))
	return r
}

// DatasourceConnectors is the host's connector registry; nil until the
// datasource connector is configured.
func (s *Service) DatasourceConnectors() *connector.Registry { return s.datasourceConnectors }

// DatasourceCatalogEntry is one registered connector as the catalog serves it:
// its descriptor, and whether a new source of it may be connected now.
type DatasourceCatalogEntry struct {
	AcceptsOperations bool
	OperationsGap     string
	OAuth             DatasourceOAuthProvider
	Descriptor        connector.Descriptor
	AcceptsNewSources bool
	// LiveDeliveryConfigured is whether THIS deployment has wired the
	// provider-level push endpoint this connector receives on. The descriptor's
	// SupportsWebhook is a property of the connector and is the same everywhere;
	// this is a property of the deployment, and without it a client cannot tell
	// "this provider has no push path" from "nobody turned it on here".
	LiveDeliveryConfigured bool
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
		gap := "connector has no operations conformance record"
		if d.Key == DatasourceProviderAPI {
			gap = ""
		}
		out = append(out, DatasourceCatalogEntry{
			Descriptor:             d,
			AcceptsOperations:      d.Key == DatasourceProviderAPI,
			OperationsGap:          gap,
			OAuth:                  s.datasourceOAuth[d.Key],
			AcceptsNewSources:      s.datasourceConnectors.AdmitNewSource(d.Key) == nil,
			LiveDeliveryConfigured: s.DatasourceLiveDeliveryConfigured(d.Key),
		})
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
	src := connector.Source{ID: source.ID, OrgID: source.OrgID, BoundaryNodeID: source.BoundaryNodeID, CredentialKey: datasourceCredentialKey(source), PersonalOwnerUserID: source.PersonalOwnerUserID}
	switch {
	case source.Provider == DatasourceProviderGitHub:
		src.Config = github.SourceConfig{Repo: source.Repo, Branch: source.Branch, InScope: datasourceFileScope(source)}
	case source.Provider == DatasourceProviderUpload && source.Upload != nil:
		cfg := source.Upload
		src.Config = objectstore.SourceConfig{
			Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, Prefix: cfg.Prefix,
			Scope: objectstore.Scope{InScope: uploadKeyScope(cfg), MaxObjects: cfg.MaxObjects},
		}
	}
	return src
}

// uploadBucketForSource resolves one object-storage source's Bucket, per call:
// the host decrypts the source's secret access key and binds it to that call,
// and the connector never sees it.
func (s *Service) uploadBucketForSource(ctx context.Context, src connector.Source) (objectstore.Bucket, error) {
	if s.newUploadBucket == nil {
		return nil, errors.New("object storage is not configured: the host composes no object-storage service")
	}
	source, err := s.store.GetDatasourceSourceByID(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if source == nil || source.OrgID != src.OrgID || source.Provider != DatasourceProviderUpload || source.Upload == nil {
		return nil, fmt.Errorf("datasource source %s is not an object-storage source of org %s", src.ID, src.OrgID)
	}
	secret, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret access key: %w", err)
	}
	cfg, _ := connectorSource(source).Config.(objectstore.SourceConfig)
	return s.newUploadBucket(cfg, source.Upload.AccessKeyID, secret), nil
}

// objectManifests retains an object-storage source's listings in the store,
// inside the source's org transaction.
type objectManifests struct{ store Store }

func (m objectManifests) Put(ctx context.Context, src connector.Source, version string, items []objectstore.Item) error {
	return m.store.WithOrgTx(ctx, src.OrgID, func(ctx context.Context) error {
		return m.store.PutDatasourceObjectManifest(ctx, src.OrgID, src.ID, version, items)
	})
}

func (m objectManifests) Get(ctx context.Context, src connector.Source, version string) (items []objectstore.Item, ok bool, err error) {
	err = m.store.WithOrgTx(ctx, src.OrgID, func(ctx context.Context) error {
		items, ok, err = m.store.GetDatasourceObjectManifest(ctx, src.OrgID, src.ID, version)
		return err
	})
	return items, ok, err
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

// Operations admission is independent of the sync envelope's conformance.
func (s *Service) admitDatasourceConnection(provider string) error {
	if provider == DatasourceProviderAPI && s.datasourceConnectors != nil {
		return nil
	}
	return s.admitNewDatasource(provider)
}
