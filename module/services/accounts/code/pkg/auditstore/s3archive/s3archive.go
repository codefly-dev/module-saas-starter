// Package s3archive writes the audit store swap's locked archive (ADR 0009) to
// an Amazon S3 bucket, or a bucket of an S3-compatible object store: one JSON
// Lines object per relay batch, named and encoded as every archive is
// (archiveformat), so it reads exactly as a GCS archive does.
//
// The writer's authority is s3:PutObject alone. It never overwrites — every
// write carries If-None-Match: * — and never deletes; the bucket's Object Lock
// default retention, which the deployment configures, is what keeps anyone
// else from doing either before the window ends. Every put carries the
// object's SHA-256, which S3 verifies before it stores the object and which an
// Object Lock bucket requires.
//
// Credentials, region and endpoint come from the AWS SDK's default chain and
// the environment only — on Kubernetes the pod's workload identity, and
// AWS_REGION, plus AWS_ENDPOINT_URL_S3 for an S3-compatible store — so there is
// deliberately no key setting of the kit's own.
package s3archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"accounts/pkg/auditstore/archiveformat"
	"accounts/pkg/business"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// objectPutter is the one S3 call the writer makes; *s3.Client satisfies it.
type objectPutter interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Archive writes relay batches as archive objects into one bucket.
type Archive struct {
	client objectPutter
	bucket string
}

// New writes into bucket through client.
func New(client *s3.Client, bucket string) (*Archive, error) {
	if client == nil {
		return nil, errors.New("s3 audit archive: client is required")
	}
	return newArchive(client, bucket)
}

func newArchive(client objectPutter, bucket string) (*Archive, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("s3 audit archive: bucket is required")
	}
	return &Archive{client: client, bucket: bucket}, nil
}

// NewFromEnvironment writes into bucket through a client the AWS SDK's
// default chain configures: credentials from the environment, the shared
// files or the platform's identity; the region from AWS_REGION; and the
// endpoint from AWS_ENDPOINT_URL_S3 (or AWS_ENDPOINT_URL) when the bucket is in
// an S3-compatible store rather than S3 itself. A missing region is refused
// here, at startup, rather than at the first delivery.
func NewFromEnvironment(ctx context.Context, bucket string) (*Archive, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("s3 audit archive: aws configuration: %w", err)
	}
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("s3 audit archive: no AWS region is configured; set AWS_REGION")
	}
	return New(s3.NewFromConfig(cfg, PathStyleForEndpointOverride), bucket)
}

// PathStyleForEndpointOverride addresses buckets by path when the endpoint
// is overridden (AWS_ENDPOINT_URL_S3): S3-compatible stores serve bucket paths
// at their one endpoint, while virtual-hosted addressing would need a DNS
// name per bucket. S3 itself, reached through its own endpoints, keeps the
// SDK's default addressing.
func PathStyleForEndpointOverride(o *s3.Options) {
	if o.BaseEndpoint != nil {
		o.UsePathStyle = true
	}
}

// WriteAuditBatch implements business.AuditArchive.
func (a *Archive) WriteAuditBatch(ctx context.Context, batch business.AuditBatch) error {
	if len(batch.Records) == 0 {
		return errors.New("s3 audit archive: refusing an empty batch")
	}
	name, err := archiveformat.ObjectName(batch)
	if err != nil {
		return err
	}
	body, err := archiveformat.Encode(batch)
	if err != nil {
		return err
	}
	if err := a.putIfAbsent(ctx, name, body); err != nil {
		return fmt.Errorf("s3 audit archive: write %s: %w", name, err)
	}
	return nil
}

// putIfAbsent uploads body in one request that S3 refuses with 412 if an
// object of that name exists, carrying the body's SHA-256 so a corrupted
// upload is refused. The SDK retries the request; if a retry finds the object
// already there, that object is this upload's own — the name carries a batch
// id no other attempt uses — so the precondition failure is the
// acknowledgement it lost, exactly as the GCS writer treats it.
func (a *Archive) putIfAbsent(ctx context.Context, name string, body []byte) error {
	sum := sha256.Sum256(body)
	_, err := a.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(a.bucket),
		Key:               aws.String(name),
		Body:              bytes.NewReader(body),
		ContentLength:     aws.Int64(int64(len(body))),
		ContentType:       aws.String(archiveformat.ContentType),
		IfNoneMatch:       aws.String("*"),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	var response interface{ HTTPStatusCode() int }
	if errors.As(err, &response) && response.HTTPStatusCode() == http.StatusPreconditionFailed {
		return nil
	}
	return err
}
