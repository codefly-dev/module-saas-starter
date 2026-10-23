package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	storagev0 "github.com/codefly-dev/service-object-storage/gen/codefly/storage/v0"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"accounts/pkg/datasource/connector"
)

// The Bucket the host reads through: the composed object-storage service
// (codefly-dev/service-object-storage), under a per-call Binding that names the
// source's store, bucket and prefix and carries its credential for that one
// call. The service owns the store protocol, every backend, the
// public-destination guard on each dial and the caps; the host keeps the
// source, the credential envelope and the retained listings.

// maxListPages bounds one listing: a prefix whose listing runs past this many
// of the service's pages is not listed whole, and a partial listing is never a
// version.
const maxListPages = 20

// ErrBindingNotHonoured: the service answered Capabilities under the binding
// without the caps a bound call reports, which is a release from before the
// binding existed. Such a service ignores the binding and serves its own
// configured bucket, so nothing it lists or reads is taken for the source's.
var ErrBindingNotHonoured = errors.New("objectstore: the object-storage service does not honour a per-call binding; it would serve its own bucket")

// ServiceBucket is one source's bucket as the object-storage service serves it.
type ServiceBucket struct {
	client     storagev0.ObjectStorageClient
	binding    *storagev0.Binding
	folder     string // the binding's prefix: every key the service reports is relative to it
	listPrefix string // the rest of the source's prefix, which a listing filters on
	honoured   bool   // Capabilities under the binding reported its caps
}

// NewServiceBucket binds cfg and the source's access key to calls on client.
// A source's prefix is a key prefix, not a folder ("reports-2026" matches
// "reports-2026-q1.pdf"), while a binding's prefix is a folder: the binding
// takes the prefix up to its last "/", and the listing filters on the rest, so
// an item's key is the full object key either way.
func NewServiceBucket(client storagev0.ObjectStorageClient, cfg SourceConfig, accessKeyID, secretAccessKey string) *ServiceBucket {
	cut := strings.LastIndex(cfg.Prefix, "/") + 1
	return &ServiceBucket{
		client: client,
		binding: &storagev0.Binding{
			// "s3" with an explicit endpoint is any S3-style store, addressed
			// path-style; the source's endpoint is required, so this never
			// silently means AWS.
			Kind:     "s3",
			Endpoint: cfg.Endpoint,
			Region:   cfg.Region,
			Bucket:   cfg.Bucket,
			Prefix:   cfg.Prefix[:cut],
			Credential: &storagev0.Credential{Kind: &storagev0.Credential_AccessKey{
				AccessKey: &storagev0.AccessKey{Id: accessKeyID, Secret: secretAccessKey},
			}},
		},
		folder:     cfg.Prefix[:cut],
		listPrefix: cfg.Prefix[cut:],
	}
}

// List pages through the prefix. A listing longer than maxListPages pages is
// ErrListingIncomplete.
func (b *ServiceBucket) List(ctx context.Context) ([]Item, error) {
	if err := b.checkBinding(ctx); err != nil {
		return nil, err
	}
	var items []Item
	token := ""
	for page := 0; page < maxListPages; page++ {
		res, err := b.client.List(ctx, &storagev0.ListRequest{Prefix: b.listPrefix, PageToken: token, Binding: b.binding})
		if err != nil {
			return nil, storeError(err)
		}
		for _, o := range res.GetObjects() {
			items = append(items, Item{Key: b.folder + o.GetKey(), ETag: o.GetEtag(), Size: o.GetSize()})
		}
		if token = res.GetNextPageToken(); token == "" {
			return items, nil
		}
	}
	return nil, fmt.Errorf("%w: more than %d pages", ErrListingIncomplete, maxListPages)
}

// Read gets each object at its listed ETag. The service's bound read takes one
// key per call, so a batch of N objects costs N calls: the gap the provider is
// registered with until the service reads a batch in one call.
func (b *ServiceBucket) Read(ctx context.Context, items []Item, visit func(Item, string, io.Reader) error) error {
	if err := b.checkBinding(ctx); err != nil {
		return err
	}
	for _, it := range items {
		content, contentType, err := b.get(ctx, it)
		if err != nil {
			return err
		}
		if err := visit(it, contentType, bytes.NewReader(content)); err != nil {
			return err
		}
	}
	return nil
}

// checkBinding asks the service, once per Bucket, what a call under the binding
// honours. A service that knows the binding reports the bound read's caps; one
// that does not drops the unknown field, answers for its configured bucket,
// and reports none — and would answer List and Get from that bucket too.
func (b *ServiceBucket) checkBinding(ctx context.Context) error {
	if b.honoured {
		return nil
	}
	caps, err := b.client.Capabilities(ctx, &storagev0.CapabilitiesRequest{Binding: b.binding})
	if err != nil {
		return storeError(err)
	}
	if caps.GetMaxObjectBytes() <= 0 || caps.GetMaxListLimit() <= 0 {
		return ErrBindingNotHonoured
	}
	b.honoured = true
	return nil
}

func (b *ServiceBucket) get(ctx context.Context, it Item) ([]byte, string, error) {
	stream, err := b.client.Get(ctx, &storagev0.GetRequest{Key: strings.TrimPrefix(it.Key, b.folder), IfMatch: it.ETag, Binding: b.binding})
	if err != nil {
		return nil, "", readError(err)
	}
	first, err := stream.Recv()
	if err != nil {
		return nil, "", readError(err)
	}
	header := first.GetHeader()
	if header == nil {
		return nil, "", errors.New("objectstore: the service sent data before the object's header")
	}
	var content bytes.Buffer
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", readError(err)
		}
		if int64(content.Len()+len(msg.GetData())) > it.Size {
			return nil, "", ErrObjectChanged
		}
		content.Write(msg.GetData())
	}
	if int64(content.Len()) != it.Size {
		return nil, "", ErrObjectChanged
	}
	return content.Bytes(), header.GetInfo().GetContentType(), nil
}

// readError is storeError for a read at a listed ETag: an object gone or
// rewritten since its listing is not at that version any more.
func readError(err error) error {
	switch status.Code(err) {
	case codes.NotFound, codes.FailedPrecondition:
		return fmt.Errorf("%w: %w", ErrObjectChanged, err)
	}
	return storeError(err)
}

// storeError types the service's status for the envelope. PERMISSION_DENIED is
// a credential the store refused or an endpoint the service's guard refused;
// both are the source's configuration to fix, and the service's message names
// which. RESOURCE_EXHAUSTED is a throttle: an object over the service's size
// cap is refused before its read, from its listed size.
func storeError(err error) error {
	st := status.Convert(err)
	switch st.Code() {
	case codes.PermissionDenied:
		return fmt.Errorf("%w: %w", connector.ErrCredentialRefused, err)
	case codes.ResourceExhausted:
		limited := &connector.RateLimitedError{Scope: "credential", Cause: err}
		for _, d := range st.Details() {
			if retry, ok := d.(*errdetails.RetryInfo); ok && retry.GetRetryDelay() != nil {
				limited.ResetAt = time.Now().Add(retry.GetRetryDelay().AsDuration())
			}
		}
		return limited
	}
	return err
}

// Dial opens a connection to the object-storage service at addr, presenting
// token as x-codefly-token on every call. The service is reached in plaintext
// inside the cell, where the mesh secures the hop; an empty token dials
// without one, which is a deployed cell's profile.
func Dial(addr, token string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(serviceToken(token)))
	}
	return grpc.NewClient(addr, opts...)
}

type serviceToken string

func (t serviceToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"x-codefly-token": string(t)}, nil
}

func (serviceToken) RequireTransportSecurity() bool { return false }
