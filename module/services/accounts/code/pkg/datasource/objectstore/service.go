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
// binding existed — such a service ignores the binding and serves its own
// configured bucket, so nothing it lists or reads is taken for the source's —
// or without a batch size, which is one from before GetMany.
var ErrBindingNotHonoured = errors.New("objectstore: the object-storage service does not honour a per-call binding with a batched read; an older one would serve its own bucket")

// ServiceBucket is one source's bucket as the object-storage service serves it.
type ServiceBucket struct {
	client     storagev0.ObjectStorageClient
	binding    *storagev0.Binding
	folder     string // the binding's prefix: every key the service reports is relative to it
	listPrefix string // the rest of the source's prefix, which a listing filters on
	maxBatch   int    // objects per GetMany, once Capabilities under the binding answered
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

// Read gets the batch at its listed ETags in one GetMany call per
// max_get_many_items objects (1000, which is the connector's batch), streamed
// back in request order: each object's header, then its bytes. An object
// rewritten or gone since its listing is ErrObjectChanged; one over the
// service's cap is the envelope's ErrItemTooLarge.
func (b *ServiceBucket) Read(ctx context.Context, items []Item, visit func(Item, string, io.Reader) error) error {
	if err := b.checkBinding(ctx); err != nil {
		return err
	}
	for start := 0; start < len(items); start += b.maxBatch {
		if err := b.readBatch(ctx, items[start:min(start+b.maxBatch, len(items))], visit); err != nil {
			return err
		}
	}
	return nil
}

func (b *ServiceBucket) readBatch(ctx context.Context, items []Item, visit func(Item, string, io.Reader) error) error {
	req := &storagev0.GetManyRequest{Binding: b.binding, Items: make([]*storagev0.GetManyItem, len(items))}
	for i, it := range items {
		req.Items[i] = &storagev0.GetManyItem{Key: strings.TrimPrefix(it.Key, b.folder), IfMatch: it.ETag}
	}
	stream, err := b.client.GetMany(ctx, req)
	if err != nil {
		return storeError(err)
	}
	var (
		current     *Item
		contentType string
		content     bytes.Buffer
		answered    int
	)
	finish := func() error {
		if current == nil {
			return nil
		}
		it := *current
		current = nil
		if int64(content.Len()) != it.Size {
			return fmt.Errorf("%w: %s", ErrObjectChanged, it.Key)
		}
		return visit(it, contentType, bytes.NewReader(content.Bytes()))
	}
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return storeError(err)
		}
		if data, ok := msg.GetKind().(*storagev0.GetManyResponse_Data); ok {
			if current == nil || int64(content.Len()+len(data.Data)) > current.Size {
				return fmt.Errorf("%w: the service sent bytes no listed object has", ErrObjectChanged)
			}
			content.Write(data.Data)
			continue
		}
		if err := finish(); err != nil {
			return err
		}
		header := msg.GetHeader()
		if int(header.GetIndex()) != answered {
			return fmt.Errorf("objectstore: the service answered object %d where %d was next", header.GetIndex(), answered)
		}
		it := items[answered]
		answered++
		switch header.GetOutcome() {
		case storagev0.GetManyOutcome_GET_MANY_OUTCOME_OK:
			current, contentType = &it, header.GetInfo().GetContentType()
			content.Reset()
		case storagev0.GetManyOutcome_GET_MANY_OUTCOME_CHANGED, storagev0.GetManyOutcome_GET_MANY_OUTCOME_NOT_FOUND:
			return fmt.Errorf("%w: %s", ErrObjectChanged, it.Key)
		case storagev0.GetManyOutcome_GET_MANY_OUTCOME_TOO_LARGE:
			return fmt.Errorf("%w: %s is over the object-storage service's cap", connector.ErrItemTooLarge, it.Key)
		default:
			return fmt.Errorf("objectstore: the service answered object %s with outcome %v", it.Key, header.GetOutcome())
		}
	}
	if err := finish(); err != nil {
		return err
	}
	if answered != len(items) {
		return fmt.Errorf("%w: the service answered %d of %d objects", ErrObjectChanged, answered, len(items))
	}
	return nil
}

// checkBinding asks the service, once per Bucket, what a call under the binding
// honours. A service that knows the binding reports the bound read's caps; one
// that does not drops the unknown field, answers for its configured bucket,
// and reports none — and would answer List and Get from that bucket too. A
// service that reports no batch size predates the batched read, which this
// client needs.
func (b *ServiceBucket) checkBinding(ctx context.Context) error {
	if b.maxBatch > 0 {
		return nil
	}
	caps, err := b.client.Capabilities(ctx, &storagev0.CapabilitiesRequest{Binding: b.binding})
	if err != nil {
		return storeError(err)
	}
	if caps.GetMaxObjectBytes() <= 0 || caps.GetMaxListLimit() <= 0 || caps.GetMaxGetManyItems() <= 0 {
		return ErrBindingNotHonoured
	}
	b.maxBatch = int(caps.GetMaxGetManyItems())
	return nil
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
