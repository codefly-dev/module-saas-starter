package business

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/objectstore"

	"github.com/codefly-dev/core/wool"
	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
)

// Object storage is the first datasource provider the host resolves from a
// composed service instead of compiling it into its registry. The registry
// built at start (newDatasourceConnectorRegistry) holds the compiled-in
// providers only; object storage is registered when, and only when, the
// declared object-storage dependency resolves to a composed service that can
// read a tenant's bucket (SetObjectStorage, called from work.go). A
// composition without that service has no object-storage provider at all: the
// catalog does not list it, AddSource refuses a source of it by name, and an
// existing source's sync refuses with the same error. Everything the host does
// for the provider is in this file; the generic sync, catalog and admission
// paths name no object-storage type.

// ErrObjectStorageUnavailable: no composed object-storage service here can read
// a tenant's bucket, so the provider is not registered and an object-storage
// source is neither admitted nor synced.
var ErrObjectStorageUnavailable = errors.New("object storage is not available here: no composed object-storage service serves a per-call binding with a batched read")

// ErrUploadDestinationRefused: the object-storage service's guard refused a
// source's endpoint. The source is parked; its endpoint must change.
var ErrUploadDestinationRefused = objectstore.ErrDestinationRefused

// uploadBucketFactory builds one object-storage source's Bucket for one call.
type uploadBucketFactory = func(cfg objectstore.SourceConfig, accessKeyID, secretAccessKey string) objectstore.Bucket

// SetObjectStorage resolves the object-storage provider from the composed
// object-storage service: every object-storage source is read through it, under
// a per-call binding of the source's bucket and credential — if, and only if,
// the service can serve one: it asks first (objectstore.CheckService). A
// service that cannot — a release from before the binding, such as 0.0.18 — is
// refused and the provider is not registered, exactly as when none is composed.
func (s *Service) SetObjectStorage(ctx context.Context, client storagev0.ObjectStorageClient) error {
	if err := objectstore.CheckService(ctx, client); err != nil {
		return err
	}
	return s.useObjectStorage(func(cfg objectstore.SourceConfig, accessKeyID, secretAccessKey string) objectstore.Bucket {
		return objectstore.NewServiceBucket(client, cfg, accessKeyID, secretAccessKey)
	})
}

// SetDatasourceUploadBucketFactory registers the object-storage provider over
// factory in place of the service. Tests use it to script a store.
func (s *Service) SetDatasourceUploadBucketFactory(factory uploadBucketFactory) {
	if err := s.useObjectStorage(factory); err != nil {
		panic(fmt.Sprintf("datasource connector registry: %v", err))
	}
}

// useObjectStorage makes factory the bucket every object-storage source reads
// and registers the provider in the host's registry, once. A registry built
// later registers it from the factory (newDatasourceConnectorRegistry).
func (s *Service) useObjectStorage(factory uploadBucketFactory) error {
	s.newUploadBucket = factory
	if s.datasourceConnectors == nil {
		return nil
	}
	return s.registerObjectStorage(s.datasourceConnectors)
}

// registerObjectStorage adds the object-storage provider to r through the
// registry's own validation, metered per source credential like GitHub. It
// passes the conformance suite as registered, over the service's wire
// (TestObjectStorageAsRegisteredPassesTheConformanceSuite).
func (s *Service) registerObjectStorage(r *connector.Registry) error {
	if _, registered := r.Descriptor(DatasourceProviderUpload); registered {
		return nil
	}
	return r.Register(connector.Budgeted(objectstore.NewFilesConnector(s.uploadBucketForSource, objectManifests{s.store}), datasourceScheduler{s: s}))
}

// uploadSourceConfig is the connector's view of a stored object-storage
// source's configuration.
func uploadSourceConfig(cfg *UploadDatasourceConfig) objectstore.SourceConfig {
	return objectstore.SourceConfig{
		Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket, Prefix: cfg.Prefix,
		Scope: objectstore.Scope{InScope: uploadKeyScope(cfg), MaxObjects: cfg.MaxObjects},
	}
}

// uploadBucketForSource resolves one object-storage source's Bucket, per call:
// the host decrypts the source's secret access key and binds it to that call,
// and the connector never sees it.
func (s *Service) uploadBucketForSource(ctx context.Context, src connector.Source) (objectstore.Bucket, error) {
	if s.newUploadBucket == nil {
		return nil, ErrObjectStorageUnavailable
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
	return s.newUploadBucket(uploadSourceConfig(source.Upload), source.Upload.AccessKeyID, secret), nil
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

// runUploadSync mirrors the source's scope into the documents store. It takes a
// complete snapshot through the object-storage connector — the listing within
// the source's key filter and object cap, read through the object-storage
// service — and fetches it in batches, enqueuing an ingest delivery per
// object, then closes the sync with the complete listing of every object it
// delivered, so the store tombstones the entries whose objects are gone. Every
// job of one sync carries one freshly allocated ordinal, which the store ranks
// the listing against the writes it covers by.
//
// The listing is sent only when the sync saw the whole scope and delivered
// every object in it: a failed listing, one the cap cuts, a fetch failure, or
// an object too large to deliver ends the sync without one, so a partial view
// can never delete. An object rewritten or deleted between the listing and its
// read fails the sync, which the next one lists afresh.
func (s *Service) runUploadSync(ctx context.Context, source *DatasourceSource) (int, error) {
	w := wool.Get(ctx).In("runUploadSync")
	// The registered connector is the metered one: every listing and batched
	// read spends the source credential's budget, as a GitHub sync does.
	conn, ok := s.datasourceConnectors.Files(DatasourceProviderUpload)
	if !ok {
		return 0, w.Wrapf(ErrObjectStorageUnavailable, "sync %s", source.ID)
	}
	budget := conn.Descriptor().Budget
	if source.Upload == nil {
		return 0, w.NewError("upload source has no config")
	}
	src := connectorSource(source)
	snapshot, err := conn.Changes(ctx, src, "")
	if errors.Is(err, objectstore.ErrListingIncomplete) {
		return 0, w.Wrapf(fmt.Errorf("%w: %w", ErrUploadListingIncomplete, err), "list objects under %q", source.Upload.Prefix)
	}
	if err != nil {
		return 0, w.Wrapf(s.parkRefusedUploadSource(ctx, source, err), "list objects")
	}
	// A host-owned invariant, whatever any service version answers: a sync
	// never closes with a snapshot that empties a source that has files. Such a
	// snapshot tombstones every entry mirrored from the source, and a store or
	// service that wrongly answers "empty" — a rewritten scope, a discarded
	// truncation — is the case that has already happened. Emptying a source is
	// an explicit operation, not a side effect of a listing.
	if len(snapshot.Changes) == 0 {
		held, err := s.uploadSourceMayHoldFiles(ctx, source)
		if err != nil {
			return 0, w.Wrapf(err, "read the source's last snapshot")
		}
		if held {
			return 0, w.Wrapf(ErrUploadEmptyListingRefused, "listing under %q", source.Upload.Prefix)
		}
	}
	ordinal, err := s.allocateOrdinal(ctx, source.ID)
	if err != nil {
		return 0, w.Wrapf(err, "allocate ordinal")
	}

	enqueued, oversized := 0, 0
	delivered := make([]uploadListingFile, 0, len(snapshot.Changes))
	var batch []connector.FileRef
	var batchBytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := conn.FetchFiles(ctx, src, snapshot.To, batch, func(f connector.File, content io.Reader) error {
			body, err := io.ReadAll(content)
			if err != nil {
				return err
			}
			fingerprint := uploadFingerprint(f.Provenance.GetItemVersion(), body)
			if err := s.enqueueUploadIngest(ctx, source, f.Locator, body, f.ContentType, fingerprint, ordinal); err != nil {
				return w.Wrapf(err, "enqueue %s", f.Locator)
			}
			delivered = append(delivered, uploadListingFile{Key: f.Locator, ETag: fingerprint})
			enqueued++
			return nil
		})
		batch, batchBytes = batch[:0], 0
		// An object the object-storage service refuses as over its cap is
		// skipped and the rest of the batch is delivered; like an object over
		// the ingest limit, it keeps this sync from sending a listing that
		// would tombstone it.
		var skipped *objectstore.TooLargeError
		if errors.As(err, &skipped) {
			oversized += len(skipped.Keys)
			return nil
		}
		if err != nil {
			return s.parkRefusedUploadSource(ctx, source, err)
		}
		return nil
	}
	for _, ch := range snapshot.Changes {
		// Objects too large for the ingest inbox cannot be delivered and the
		// consumer holds no credentials to re-fetch them: never read, counted.
		if ch.Size > maxIngestPayload {
			oversized++
			continue
		}
		if len(batch) == budget.MaxItemsPerCall || batchBytes+ch.Size > budget.MaxBytesPerCall {
			if err := flush(); err != nil {
				return enqueued, w.Wrapf(err, "fetch objects")
			}
		}
		batch = append(batch, connector.FileRef{ItemID: ch.Locator, ItemVersion: ch.ItemVersion})
		batchBytes += ch.Size
	}
	if err := flush(); err != nil {
		return enqueued, w.Wrapf(err, "fetch objects")
	}

	if oversized > 0 {
		return enqueued, w.NewError("%d object(s) exceed the ingest payload limit", oversized)
	}
	if err := s.enqueueUploadListing(ctx, source, ordinal, delivered); err != nil {
		return enqueued, w.Wrapf(err, "enqueue listing")
	}

	if err := s.store.WithOrgTx(ctx, source.OrgID, func(ctx context.Context) error {
		return s.store.RecordDatasourceSnapshotSent(ctx, source.OrgID, source.ID, time.Now().UTC(), len(delivered))
	}); err != nil {
		return enqueued, w.Wrapf(err, "record sync time")
	}
	return enqueued, nil
}

// parkRefusedUploadSource parks an object-storage source a retry cannot fix:
// the object-storage service's guard refused its endpoint, or its store
// rejected its access key. The source is degraded with a reason a person can
// act on — change the endpoint, or connect it again with a valid key — so the
// reconcile sweep stops retrying it. Any other error is returned as it is. A
// park that fails returns its own error in place of the refusal, so the job
// retries and the park is attempted again (parkDatasourceSource).
func (s *Service) parkRefusedUploadSource(ctx context.Context, source *DatasourceSource, err error) error {
	var reason DatasourceDegradeReason
	switch {
	case errors.Is(err, objectstore.ErrDestinationRefused):
		reason = ObjectStorageEndpointRefusedDegradeReason()
	case errors.Is(err, connector.ErrCredentialRefused):
		reason = ObjectStorageCredentialRejectedDegradeReason()
	default:
		return err
	}
	if _, perr := s.parkDatasourceSource(ctx, source, reason); perr != nil {
		wool.Get(ctx).Warn("could not park a refused object-storage source", wool.Field("source_id", source.ID), wool.ErrField(perr))
		return fmt.Errorf("park the refused object-storage source: %w", perr)
	}
	return err
}
