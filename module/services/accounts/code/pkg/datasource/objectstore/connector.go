package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"accounts/pkg/datasource/connector"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

// The object-storage files connector, on the envelope. An object store has no
// "what changed since": it answers only "what is there now". So the version of
// a source is a digest of its listing (key → ETag → size), the host retains the
// listing each version names (Manifests), and Changes diffs the current listing
// against the retained one. The listing and the reads go through a Bucket, which
// is the composed object-storage service under the source's binding
// (codefly-dev/service-object-storage#108): this connector signs nothing, dials
// nothing and guards nothing itself.

// ConnectorKey is the provider key object-storage sources are stored under.
const ConnectorKey = "upload"

// Budget limits. An object past MaxItemBytes is refused, never truncated.
const (
	MaxObjectsPerBatch = 1000
	MaxBatchBytes      = 64 << 20
	MaxObjectBytes     = 5 << 20
)

// estimatedThrottleWindow is the reset a throttled call is given when the store
// does not say when its limit resets.
const estimatedThrottleWindow = time.Minute

// SourceConfig is where one source reads: its store, bucket and key prefix. The
// credential is not here; the host hands it to the Bucket per call.
type SourceConfig struct {
	Endpoint string
	Region   string
	Bucket   string
	Prefix   string
}

// binding names the configuration a version was listed under, so a version of
// one bucket or prefix is never diffed against, or read from, another.
func (c SourceConfig) binding() string {
	sum := sha256.Sum256([]byte(c.Endpoint + "\x00" + c.Region + "\x00" + c.Bucket + "\x00" + c.Prefix))
	return hex.EncodeToString(sum[:8])
}

// Item is one object of a listing: its full key (the item's stable id), its
// ETag (the item's version) and its exact size in bytes.
type Item struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

// ErrListingIncomplete: the store could not list the whole prefix (it truncated
// the listing, or the listing passed the store's cap). A partial listing is never
// a version: it would read every unlisted object as deleted.
var ErrListingIncomplete = errors.New("objectstore: the store did not list the whole prefix")

// ErrObjectChanged: an object named at an ETag no longer has it (it was
// rewritten or deleted after it was listed).
var ErrObjectChanged = errors.New("objectstore: the object is no longer at the listed version")

// Bucket is the store as the connector reads it, for one source. It reports a
// throttle as *connector.RateLimitedError and a refused credential as
// connector.ErrCredentialRefused.
type Bucket interface {
	// List returns every object under the source's prefix, or
	// ErrListingIncomplete; never a partial listing.
	List(ctx context.Context) ([]Item, error)
	// Read streams the named objects in one call, in order, each only at its
	// listed ETag (ErrObjectChanged otherwise), with a reader of exactly its size.
	Read(ctx context.Context, items []Item, visit func(item Item, contentType string, content io.Reader) error) error
}

// BucketFunc resolves the Bucket of one source, per call: the host decrypts the
// source's credential and binds it, and the connector never sees it.
type BucketFunc func(ctx context.Context, src connector.Source) (Bucket, error)

// Manifests is the host's retained listings, per source and version. Put is
// idempotent; Get reports false for a version that was never retained or has
// since been pruned.
type Manifests interface {
	Put(ctx context.Context, src connector.Source, version string, items []Item) error
	Get(ctx context.Context, src connector.Source, version string) ([]Item, bool, error)
}

// FilesConnector is the object-storage connector.
type FilesConnector struct {
	bucket    BucketFunc
	manifests Manifests
	budget    connector.Budget
	now       func() time.Time
}

// ConnectorOption configures a FilesConnector.
type ConnectorOption func(*FilesConnector)

// WithMaxItemBytes lowers the per-object limit, for a test that must script an
// over-limit object cheaply. It never raises it past MaxObjectBytes.
func WithMaxItemBytes(n int64) ConnectorOption {
	return func(c *FilesConnector) {
		if n > 0 && n < c.budget.MaxItemBytes {
			c.budget.MaxItemBytes = n
		}
	}
}

// NewFilesConnector returns the object-storage connector over bucket, retaining
// listings in manifests.
func NewFilesConnector(bucket BucketFunc, manifests Manifests, opts ...ConnectorOption) *FilesConnector {
	c := &FilesConnector{
		bucket:    bucket,
		manifests: manifests,
		budget: connector.Budget{
			MaxItemsPerCall: MaxObjectsPerBatch, MaxBytesPerCall: MaxBatchBytes, MaxItemBytes: MaxObjectBytes,
			// One operation is one listing or one batched read. A store meters
			// requests per second, far above this; the quota is what the host
			// spends on a tenant's bucket, with a fifth kept for a person's sync.
			OperationsPerWindow: 1200, Window: time.Hour, BackgroundSharePercent: 80,
		},
		now: time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Descriptor is the connector's catalog entry.
func (c *FilesConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Key:         ConnectorKey,
		DisplayName: "Object storage",
		Description: "A bucket in an object store; the objects under a key prefix are pulled on sync, and only what changed is re-sent.",
		Interface:   connector.InterfaceFiles,
		ConfigFields: []connector.ConfigField{
			{Key: "endpoint", DisplayName: "Endpoint", Help: "The store's https base URL.", Required: true},
			{Key: "region", DisplayName: "Region", Help: "The bucket's region, e.g. us-east-1.", Required: true},
			{Key: "bucket", DisplayName: "Bucket", Required: true},
			{Key: "prefix", DisplayName: "Prefix", Help: "Key prefix to read under; empty means the whole bucket.", Required: false},
			{Key: "access_key_id", DisplayName: "Access key ID", Required: true},
		},
		CredentialModes: []connector.CredentialMode{connector.CredentialStaticSecret},
		// A bucket has no per-object access list: every object is readable by
		// whoever may read the source's collection. One collection per prefix.
		Readers:    connector.ReadersSourceScoped,
		Budget:     c.budget,
		Conformant: true,
	}
}

func (c *FilesConnector) open(ctx context.Context, src connector.Source) (SourceConfig, Bucket, error) {
	cfg, ok := src.Config.(SourceConfig)
	if !ok || cfg.Bucket == "" {
		return SourceConfig{}, nil, fmt.Errorf("objectstore connector: source %s has no object-storage configuration", src.ID)
	}
	if err := ctx.Err(); err != nil {
		return SourceConfig{}, nil, err
	}
	bucket, err := c.bucket(ctx, src)
	if err != nil {
		return SourceConfig{}, nil, c.envelopeError(err)
	}
	return cfg, bucket, nil
}

// list reads the current listing and its version. Folder markers (keys ending
// in "/") are not files and are left out.
func (c *FilesConnector) list(ctx context.Context, cfg SourceConfig, bucket Bucket) (string, map[string]Item, error) {
	items, err := bucket.List(ctx)
	if err != nil {
		return "", nil, c.envelopeError(err)
	}
	listed := make(map[string]Item, len(items))
	for _, it := range items {
		if !strings.HasSuffix(it.Key, "/") {
			listed[it.Key] = it
		}
	}
	return versionOf(cfg, listed), listed, nil
}

// versionOf is the listing's digest under its binding: the binding's digest,
// then the digest of every key, ETag and size in key order.
func versionOf(cfg SourceConfig, listed map[string]Item) string {
	h := sha256.New()
	for _, key := range sortedKeys(listed) {
		it := listed[key]
		fmt.Fprintf(h, "%s\x00%s\x00%d\n", it.Key, it.ETag, it.Size)
	}
	return cfg.binding() + "." + hex.EncodeToString(h.Sum(nil))
}

func sortedKeys(m map[string]Item) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Version lists the prefix and digests it. An object store answers no cheaper
// question than its listing, so this costs one listing call.
func (c *FilesConnector) Version(ctx context.Context, src connector.Source) (string, error) {
	cfg, bucket, err := c.open(ctx, src)
	if err != nil {
		return "", err
	}
	version, _, err := c.list(ctx, cfg, bucket)
	return version, err
}

// Changes diffs the current listing against the retained listing from names,
// and retains the current one under its version. An empty from is a complete
// snapshot. A from that was listed under another binding, or whose listing is
// no longer retained, requires a resync.
//
// An object store has no rename: a key that disappeared and a key that appeared
// with the same ETag and size are the same content moved, and are reported as
// one MOVED change, paired in key order.
func (c *FilesConnector) Changes(ctx context.Context, src connector.Source, from string) (connector.ChangeSet, error) {
	cfg, bucket, err := c.open(ctx, src)
	if err != nil {
		return connector.ChangeSet{}, err
	}
	var previous map[string]Item
	if from != "" {
		if !strings.HasPrefix(from, cfg.binding()+".") {
			return connector.ChangeSet{}, connector.ErrResyncRequired
		}
		items, ok, err := c.manifests.Get(ctx, src, from)
		if err != nil {
			return connector.ChangeSet{}, err
		}
		if !ok {
			return connector.ChangeSet{}, connector.ErrResyncRequired
		}
		previous = make(map[string]Item, len(items))
		for _, it := range items {
			previous[it.Key] = it
		}
	}
	version, listed, err := c.list(ctx, cfg, bucket)
	if err != nil {
		return connector.ChangeSet{}, err
	}
	set := connector.ChangeSet{SourceID: src.ID, From: from, To: version, Complete: from == ""}
	if version == from {
		return set, nil
	}
	current := make([]Item, 0, len(listed))
	for _, key := range sortedKeys(listed) {
		current = append(current, listed[key])
	}
	if err := c.manifests.Put(ctx, src, version, current); err != nil {
		return connector.ChangeSet{}, err
	}
	readers := connector.ApplySourcePolicy(src, connector.SourceScoped())
	change := func(it Item, kind connector.ChangeKind) connector.Change {
		return connector.Change{Key: connector.KeyFor(src, it.Key), Kind: kind, ItemVersion: it.ETag, Size: it.Size, Locator: it.Key, Readers: readers}
	}
	var added, deleted []Item
	for _, it := range current {
		prev, ok := previous[it.Key]
		switch {
		case !ok:
			added = append(added, it)
		case prev.ETag != it.ETag || prev.Size != it.Size:
			set.Changes = append(set.Changes, change(it, connector.ChangeModified))
		}
	}
	for _, key := range sortedKeys(previous) {
		if _, ok := listed[key]; !ok {
			deleted = append(deleted, previous[key])
		}
	}
	moved := make(map[string]bool)
	for _, it := range added {
		kind, prevKey := connector.ChangeAdded, ""
		for _, gone := range deleted {
			if !moved[gone.Key] && gone.ETag == it.ETag && gone.Size == it.Size {
				moved[gone.Key], kind, prevKey = true, connector.ChangeMoved, gone.Key
				break
			}
		}
		ch := change(it, kind)
		ch.PreviousItemID = prevKey
		set.Changes = append(set.Changes, ch)
	}
	for _, gone := range deleted {
		if !moved[gone.Key] {
			set.Changes = append(set.Changes, connector.Change{Key: connector.KeyFor(src, gone.Key), Kind: connector.ChangeDeleted, Locator: gone.Key})
		}
	}
	return set, nil
}

// FetchFiles reads the named objects as the retained listing of version names
// them, in one batched read. An object rewritten or deleted since that listing
// is not at that version any more, and is ErrItemNotFound.
func (c *FilesConnector) FetchFiles(ctx context.Context, src connector.Source, version string, refs []connector.FileRef, visit func(connector.File, io.Reader) error) error {
	if len(refs) == 0 || len(refs) > c.budget.MaxItemsPerCall {
		return fmt.Errorf("%w: a batch names 1 to %d objects", connector.ErrBatchTooLarge, c.budget.MaxItemsPerCall)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.ItemID == "" || seen[ref.ItemID] {
			return errors.New("objectstore connector: every object of a batch names a distinct item")
		}
		seen[ref.ItemID] = true
	}
	cfg, bucket, err := c.open(ctx, src)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(version, cfg.binding()+".") {
		return connector.ErrVersionNotFound
	}
	retained, ok, err := c.manifests.Get(ctx, src, version)
	if err != nil {
		return err
	}
	if !ok {
		return connector.ErrVersionNotFound
	}
	byKey := make(map[string]Item, len(retained))
	for _, it := range retained {
		byKey[it.Key] = it
	}
	items := make([]Item, len(refs))
	var total int64
	for i, ref := range refs {
		it, ok := byKey[ref.ItemID]
		if !ok || (ref.ItemVersion != "" && ref.ItemVersion != it.ETag) {
			return connector.ErrItemNotFound
		}
		if it.Size > c.budget.MaxItemBytes {
			return fmt.Errorf("%w: an object is over %d bytes", connector.ErrItemTooLarge, c.budget.MaxItemBytes)
		}
		items[i], total = it, total+it.Size
	}
	if total > c.budget.MaxBytesPerCall {
		return fmt.Errorf("%w: the batch is %d bytes, over %d; request fewer objects", connector.ErrBatchTooLarge, total, c.budget.MaxBytesPerCall)
	}
	readers := connector.ApplySourcePolicy(src, connector.SourceScoped())
	err = bucket.Read(ctx, items, func(it Item, contentType string, content io.Reader) error {
		return visit(connector.File{
			Provenance: &accountsv1.DatasourceProvenance{
				SourceId: src.ID, OrgId: src.OrgID, BoundaryNodeId: src.BoundaryNodeID,
				Version: version, ItemId: it.Key, ItemVersion: it.ETag,
			},
			Locator:     it.Key,
			ContentType: contentType,
			Size:        it.Size,
			Readers:     readers,
		}, content)
	})
	if errors.Is(err, ErrObjectChanged) {
		return fmt.Errorf("%w: %w", connector.ErrItemNotFound, err)
	}
	return c.envelopeError(err)
}

// envelopeError gives a throttle the store did not time a reset, keeping every
// other error, typed by the Bucket, as it is.
func (c *FilesConnector) envelopeError(err error) error {
	var limited *connector.RateLimitedError
	if errors.As(err, &limited) && limited.ResetAt.IsZero() {
		limited.ResetAt, limited.Estimated = c.now().Add(estimatedThrottleWindow), true
	}
	return err
}
