// Package connector is the host's datasource connector model: the one envelope
// every connector honours, the interfaces a connector implements, and the
// descriptor-driven registry the host connects sources through.
//
// The envelope, clause by clause:
//
//  1. Identity and tenancy: every item names its source, org, boundary and the
//     provider's own stable id for it (Key).
//  2. Permissions: every item carries its readers in host terms
//     (accountsv1.DatasourceItemReaders), and anything that cannot be
//     translated fails closed to the boundary's administrators (readers.go).
//  3. Credentials: a connector obtains its credential from the host per call
//     and never returns it — not in an item, a change, an error or a descriptor.
//  4. Versions and change sets: versions are opaque; Changes goes from one to
//     the next, and a complete snapshot deletes whatever it does not list.
//  5. Provenance: every fetched item carries source, version and provider id.
//  6. Budget and backpressure: a connector declares its limits and reports a
//     provider rate limit as a RateLimitedError with its reset time.
//  7. Audit: the host emits one vocabulary for every connector; a connector
//     emits nothing itself.
//  8. Bulk only: no operation takes a single item.
//
// The conformance suite in connectortest holds a connector to all of it.
package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

// Interface is one of the five connector interfaces. The set is closed: a new
// interface is a contract change. The set of providers is open.
type Interface string

const (
	InterfaceFiles    Interface = "files"
	InterfacePages    Interface = "pages"
	InterfaceRecords  Interface = "records"
	InterfaceMessages Interface = "messages"
	InterfaceEvents   Interface = "events"
)

// ItemShape is what an interface's items are, so the host hands each to the
// module that consumes that shape without knowing which module that is:
// content (bytes with a media type, a path or a hierarchy) or structured (typed
// fields a schema describes).
type ItemShape string

const (
	ShapeContent    ItemShape = "content"
	ShapeStructured ItemShape = "structured"
)

// Shapes reports what an interface's items are: Files and Pages are content,
// Records and Events structured, and Messages both (the body and attachments
// as content, the message as a structured item citing them).
func (i Interface) Shapes() []ItemShape {
	switch i {
	case InterfaceFiles, InterfacePages:
		return []ItemShape{ShapeContent}
	case InterfaceRecords, InterfaceEvents:
		return []ItemShape{ShapeStructured}
	case InterfaceMessages:
		return []ItemShape{ShapeContent, ShapeStructured}
	}
	return nil
}

func (i Interface) valid() bool { return len(i.Shapes()) > 0 }

// Source is what the host hands a connector for one connected source: its
// identity and tenancy, and its provider config. It never carries a
// credential; a connector resolves one through the host per call.
type Source struct {
	ID             string
	OrgID          string
	BoundaryNodeID string
	// PersonalOwnerUserID is set for a source a person connected with their
	// own OAuth grant. Such a source is readable only by that person, whatever
	// the provider's sharing says (see ApplySourcePolicy).
	PersonalOwnerUserID string
	// Config is the connector's own non-secret configuration type.
	Config any
}

// Key is clause 1: where an item lives in the host, and the provider's stable
// id for it. The id survives an edit; whether it survives a move is the
// provider's (a git path does not, and a move is reported as MOVED).
type Key struct {
	SourceID       string
	OrgID          string
	BoundaryNodeID string
	ItemID         string
}

// KeyFor builds the key of a provider item of src.
func KeyFor(src Source, itemID string) Key {
	return Key{SourceID: src.ID, OrgID: src.OrgID, BoundaryNodeID: src.BoundaryNodeID, ItemID: itemID}
}

// ChangeKind is what happened to one item between two versions.
type ChangeKind string

const (
	ChangeAdded          ChangeKind = "added"
	ChangeModified       ChangeKind = "modified"
	ChangeDeleted        ChangeKind = "deleted"
	ChangeMoved          ChangeKind = "moved"
	ChangeReadersChanged ChangeKind = "readers_changed"
)

// Change is one item's change. ItemVersion is the provider's opaque version of
// the item (empty on DELETED). PreviousItemID is set on MOVED only. Readers is
// required on every kind but DELETED. Size is the item's exact byte length when
// the connector knows it at listing, and -1 when it does not; it is 0 on
// DELETED.
type Change struct {
	Key            Key
	Kind           ChangeKind
	ItemVersion    string
	PreviousItemID string
	Size           int64
	// Locator is the item's path, URL or title, for people. It is not identity.
	Locator string
	Readers *accountsv1.DatasourceItemReaders
}

// ChangeSet is clause 4: the changes from one opaque version to the next. An
// empty From is a first snapshot. Complete means the set lists every item at
// To, so any item it does not list is deleted.
type ChangeSet struct {
	SourceID string
	From     string
	To       string
	Complete bool
	Changes  []Change
}

// IdempotencyKey names a change set so a redelivery is recognisable: the same
// source, from and to always yield the same key.
func (c ChangeSet) IdempotencyKey() string {
	sum := sha256.Sum256([]byte(c.SourceID + "\x00" + c.From + "\x00" + c.To))
	return hex.EncodeToString(sum[:])
}

// FileRef names one file of a bulk fetch: its stable id, and optionally the
// item version the caller listed it at (a mismatch is ErrItemNotFound).
type FileRef struct {
	ItemID      string
	ItemVersion string
}

// File is one fetched file. Provenance is clause 5: its item_id is the stable
// id and its item_version the provider's version of the file's content.
type File struct {
	Provenance  *accountsv1.DatasourceProvenance
	Locator     string
	ContentType string
	Size        int64
	Readers     *accountsv1.DatasourceItemReaders
}

// Connector is what every connector implements whatever its interface.
type Connector interface {
	Descriptor() Descriptor
	// Version is the provider's current opaque version of the source, cheaply:
	// it lists nothing. Changes from "" to that version is the source's
	// complete snapshot.
	Version(ctx context.Context, src Source) (string, error)
	// Changes returns the changes from version from to the provider's current
	// version. An empty from returns a complete snapshot. A from the connector
	// can no longer diff against returns ErrResyncRequired, and the caller asks
	// again with an empty from.
	Changes(ctx context.Context, src Source, from string) (ChangeSet, error)
}

// FilesConnector is the Files interface: its bulk fetch serves up to
// Budget.MaxItemsPerCall files of one version per call, handing visit each in
// request order with a reader of exactly its size. A batch it cannot serve
// whole is refused before visit is first called.
type FilesConnector interface {
	Connector
	FetchFiles(ctx context.Context, src Source, version string, refs []FileRef, visit func(File, io.Reader) error) error
}

// Typed refusals every connector returns, so the host maps them to one status
// vocabulary whatever the provider.
var (
	// ErrResyncRequired: the connector cannot diff from the given version (it
	// is unknown, expired, or history was rewritten). Ask again from "".
	ErrResyncRequired = errors.New("connector: the source must be resynchronised from a complete snapshot")
	// ErrVersionNotFound: the provider does not hold the named version.
	ErrVersionNotFound = errors.New("connector: the provider does not hold that version")
	// ErrItemNotFound: a named item is not in the source's scope at that
	// version (or not at that item version). A deleted item is this.
	ErrItemNotFound = errors.New("connector: a named item is not in the source's scope at that version")
	// ErrItemTooLarge: an item exceeds Budget.MaxItemBytes. It is refused,
	// never truncated.
	ErrItemTooLarge = errors.New("connector: an item exceeds the connector's item size limit")
	// ErrBatchTooLarge: the batch names too many items or too many bytes.
	ErrBatchTooLarge = errors.New("connector: the batch exceeds the connector's per-call limits")
	// ErrCredentialRefused: the provider refused the host's credential; the
	// source must be reconnected.
	ErrCredentialRefused = errors.New("connector: the provider refused the source's credential")
)

// RateLimitedError is clause 6's typed rate limit: when the provider's limit
// resets, and whether that time is the provider's word or the connector's
// estimate (a transport that does not say).
type RateLimitedError struct {
	ResetAt   time.Time
	Estimated bool
	// Scope is what the limit meters: "source", "credential" or "deployment".
	Scope string
	// Cause is the provider's own error, kept in the chain for the host.
	Cause error
}

func (e *RateLimitedError) Unwrap() error { return e.Cause }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("connector: the provider rate limited the %s until %s", e.Scope, e.ResetAt.UTC().Format(time.RFC3339))
}
