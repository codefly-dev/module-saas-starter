package adapters

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// recordingBlobStream captures the frames writeDatasourceBlobFrames emits, and
// can be told to fail on the Nth Send so error propagation is observable.
type recordingBlobStream struct {
	frames  []*gen.FetchDatasourceBlobChunk
	failAt  int // 1-based index of the Send that returns errSend; 0 disables
	errSend error
}

func (r *recordingBlobStream) Send(chunk *gen.FetchDatasourceBlobChunk) error {
	r.frames = append(r.frames, chunk)
	if r.failAt > 0 && len(r.frames) == r.failAt {
		return r.errSend
	}
	return nil
}

func TestWriteDatasourceBlobFrames(t *testing.T) {
	cases := map[string]int{
		"empty":           0,
		"single partial":  10,
		"exact one frame": datasourceBlobChunkBytes,
		"one and a bit":   datasourceBlobChunkBytes + 1,
		"exact multiple":  datasourceBlobChunkBytes * 2,
	}
	for name, size := range cases {
		t.Run(name, func(t *testing.T) {
			content := bytes.Repeat([]byte{0xab}, size)
			stream := &recordingBlobStream{}
			if err := writeDatasourceBlobFrames(content, "application/octet-stream", stream); err != nil {
				t.Fatalf("write: %v", err)
			}

			// Every blob yields at least one frame so metadata always arrives,
			// and no trailing empty frame is emitted for exact multiples.
			wantFrames := size/datasourceBlobChunkBytes + 1
			if size > 0 && size%datasourceBlobChunkBytes == 0 {
				wantFrames = size / datasourceBlobChunkBytes
			}
			if len(stream.frames) != wantFrames {
				t.Fatalf("expected %d frames, got %d", wantFrames, len(stream.frames))
			}

			var reassembled []byte
			for _, f := range stream.frames {
				if f.GetTotalSize() != int64(size) {
					t.Fatalf("frame total size = %d, want %d", f.GetTotalSize(), size)
				}
				if f.GetContentType() != "application/octet-stream" {
					t.Fatalf("frame content type = %q", f.GetContentType())
				}
				if len(f.GetData()) > datasourceBlobChunkBytes {
					t.Fatalf("frame of %d bytes exceeds the %d cap", len(f.GetData()), datasourceBlobChunkBytes)
				}
				reassembled = append(reassembled, f.GetData()...)
			}
			if !bytes.Equal(reassembled, content) {
				t.Fatalf("reassembled %d bytes, want %d", len(reassembled), size)
			}
		})
	}
}

// TestWriteDatasourceBlobFrames_SendErrorPropagates pins that a transport send
// failure aborts the stream rather than being swallowed.
func TestWriteDatasourceBlobFrames_SendErrorPropagates(t *testing.T) {
	content := bytes.Repeat([]byte{0x01}, datasourceBlobChunkBytes*2)
	sendErr := errors.New("connection reset")
	stream := &recordingBlobStream{failAt: 1, errSend: sendErr}

	err := writeDatasourceBlobFrames(content, "application/octet-stream", stream)
	if !errors.Is(err, sendErr) {
		t.Fatalf("expected send error to propagate, got %v", err)
	}
	if len(stream.frames) != 1 {
		t.Fatalf("streaming should stop after the failed send, got %d frames", len(stream.frames))
	}
}

// ---------------------------------------------------------------------------
// streamDatasourceBlob: caller authentication and end-to-end argument wiring.
// writeDatasourceBlobFrames above covers framing in isolation; these cover the
// glue streamDatasourceBlob adds — the auth gate and passing (sourceId, blobSha)
// to the business layer in the right order — which framing tests never exercise.
// ---------------------------------------------------------------------------

const (
	// A valid v4 UUID: FetchDatasourceBlobRequest.source_id carries a string.uuid
	// constraint, so the request must pass Validate before the auth gate is what
	// rejects it.
	blobStreamSourceID = "11111111-1111-4111-8111-111111111111"
	blobStreamBlobSHA  = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	blobStreamCallerID = "22222222-2222-4222-8222-222222222222"
	blobStreamOrgID    = "33333333-3333-4333-8333-333333333333"
	blobStreamRepoOrg  = "44444444-4444-4444-8444-444444444444"
)

// blobStreamStore serves only the single source-by-id lookup
// ModuleFetchDatasourceBlob makes; every other Store method is left to panic so
// an unexpected call is loud rather than silently wrong.
type blobStreamStore struct {
	business.Store
	source *business.DatasourceSource
}

func (s *blobStreamStore) GetDatasourceSourceByID(_ context.Context, id string) (*business.DatasourceSource, error) {
	if s.source != nil && s.source.ID == id {
		return s.source, nil
	}
	return nil, nil
}

// staticCipher decrypts the source credential to a fixed token regardless of
// purpose — the blob path only needs a token to hand the GitHub client.
type staticCipher struct{ token string }

func (staticCipher) EncryptSecret(context.Context, string, string) (string, error) { return "", nil }
func (c staticCipher) DecryptSecret(context.Context, string, string) (string, error) {
	return c.token, nil
}

// blobStreamGitHub answers GetBlob for exactly one repo and rejects fetches
// aimed anywhere else, so a fetch against the wrong repo surfaces as an error.
type blobStreamGitHub struct {
	repo  string
	blobs map[string][]byte
}

func (*blobStreamGitHub) DefaultBranch(context.Context, string) (string, error) {
	return "", errors.New("unused")
}
func (*blobStreamGitHub) ResolveCommit(context.Context, string, string) (string, error) {
	return "", errors.New("unused")
}
func (*blobStreamGitHub) ListFiles(context.Context, string, string, []string) ([]github.File, error) {
	return nil, errors.New("unused")
}
func (*blobStreamGitHub) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, errors.New("unused")
}
func (*blobStreamGitHub) Compare(context.Context, string, string, string) (*github.Comparison, error) {
	return nil, errors.New("unused")
}
func (g *blobStreamGitHub) GetBlob(_ context.Context, repo, blobSHA string, _ int64) ([]byte, error) {
	if repo != g.repo {
		return nil, errors.New("blob fetched from the wrong repo: " + repo)
	}
	if b, ok := g.blobs[blobSHA]; ok {
		return b, nil
	}
	return nil, github.ErrNotFound
}

func installBlobStreamService(t *testing.T, store business.Store, cipher business.SecretCipher, gh business.GitHubContentClient, registry business.ModulePrincipalRegistry) {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetDatasourceConnector(cipher, nil, "")
	// Override the real github.New factory SetDatasourceConnector installed, so the
	// fetch never leaves the process. Must come after the connector, which only
	// seeds the factory when it is still nil.
	svc.SetDatasourceGitHubClientFactory(func(string) business.GitHubContentClient { return gh })
	svc.SetModuleCapabilities(nil, nil, registry)
	service = svc
	t.Cleanup(func() { service = previous })
}

// TestFetchDatasourceBlob_RejectsUnauthenticatedCaller pins that the adapter
// authenticates the caller before consulting the service: a well-formed request
// carrying no identity is rejected Unauthenticated with no frame sent, so an
// unauthenticated caller can never reach ModuleFetchDatasourceBlob.
func TestFetchDatasourceBlob_RejectsUnauthenticatedCaller(t *testing.T) {
	installBlobStreamService(t, &blobStreamStore{}, staticCipher{}, &blobStreamGitHub{}, business.ModulePrincipalRegistry{})

	req := &gen.FetchDatasourceBlobRequest{SourceId: blobStreamSourceID, BlobSha: blobStreamBlobSHA}
	stream := &recordingBlobStream{}

	err := streamDatasourceBlob(context.Background(), req, stream)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
	if len(stream.frames) != 0 {
		t.Fatalf("no frame must be sent for an unauthenticated caller, got %d", len(stream.frames))
	}
}

// TestFetchDatasourceBlob_StreamsAuthorizedBlobEndToEnd drives the whole adapter
// path — authenticate the caller, authorize the grant, resolve the source, fetch
// the blob, frame it — for an authorized cross-tenant module principal. The
// source id and blob sha are deliberately distinct so a transposition of the two
// arguments would look up a source that does not exist and fail NotFound instead
// of streaming the blob.
func TestFetchDatasourceBlob_StreamsAuthorizedBlobEndToEnd(t *testing.T) {
	// Larger than one frame so the multi-frame path and its constant per-frame
	// metadata are exercised end to end, not just the single-frame case.
	blob := bytes.Repeat([]byte("codefly-blob-"), 40000)
	source := &business.DatasourceSource{
		ID:                  blobStreamSourceID,
		OrgID:               blobStreamRepoOrg, // a different org than the caller's bound org: cross-tenant fetch
		Provider:            business.DatasourceProviderGitHub,
		Repo:                "acme/docs",
		CredentialSecretRef: "enc-token",
	}
	store := &blobStreamStore{source: source}
	gh := &blobStreamGitHub{repo: "acme/docs", blobs: map[string][]byte{blobStreamBlobSHA: blob}}
	registry := business.ModulePrincipalRegistry{
		blobStreamCallerID: {Queues: []string{"datasource"}, CrossTenant: true},
	}
	installBlobStreamService(t, store, staticCipher{token: "gh-token"}, gh, registry)

	ctx := stampVerifiedIdentity(context.Background(), blobStreamCallerID, blobStreamOrgID, auth.Assurance{})
	req := &gen.FetchDatasourceBlobRequest{SourceId: blobStreamSourceID, BlobSha: blobStreamBlobSHA}
	stream := &recordingBlobStream{}

	if err := streamDatasourceBlob(ctx, req, stream); err != nil {
		t.Fatalf("stream authorized blob: %v", err)
	}
	if len(stream.frames) == 0 {
		t.Fatal("an authorized fetch must stream at least one frame")
	}

	var reassembled []byte
	contentType := stream.frames[0].GetContentType()
	for _, f := range stream.frames {
		if f.GetTotalSize() != int64(len(blob)) {
			t.Fatalf("frame total size = %d, want %d", f.GetTotalSize(), len(blob))
		}
		if f.GetContentType() != contentType {
			t.Fatalf("content type changed mid-stream: %q vs %q", f.GetContentType(), contentType)
		}
		reassembled = append(reassembled, f.GetData()...)
	}
	if !bytes.Equal(reassembled, blob) {
		t.Fatalf("reassembled %d bytes, want the seeded %d", len(reassembled), len(blob))
	}
	if contentType == "" {
		t.Fatal("streamed frames carried no content type")
	}
}
