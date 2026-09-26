package adapters

import (
	"bytes"
	"errors"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type recordingFilesStream struct{ frames []*gen.FetchDatasourceFilesFrame }

func (r *recordingFilesStream) Send(frame *gen.FetchDatasourceFilesFrame) error {
	r.frames = append(r.frames, frame)
	return nil
}

const (
	filesVersion = "0123456789abcdef0123456789abcdef01234567"
	filesBigID   = "1111111111111111111111111111111111111111"
	filesEmptyID = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
)

// A batch streams as, per file in request order, one header frame carrying the
// envelope and exact size, then bounded data frames; an empty file has none.
func TestFetchDatasourceFiles_FramesEachFileBehindItsHeader(t *testing.T) {
	big := bytes.Repeat([]byte("codefly-files-"), 40000) // > 2 frames
	source := &business.DatasourceSource{
		ID: blobStreamSourceID, OrgID: blobStreamRepoOrg, BoundaryNodeID: "boundary-1",
		Provider: business.DatasourceProviderGitHub, Repo: "acme/docs", CredentialSecretRef: "enc-token",
	}
	gh := &blobStreamGitHub{
		repo:  "acme/docs",
		blobs: map[string][]byte{filesBigID: big, filesEmptyID: {}},
		files: []github.File{{Path: "docs/big.md", SHA: filesBigID}, {Path: "docs/empty.md", SHA: filesEmptyID}},
	}
	installBlobStreamService(t, &blobStreamStore{source: source}, staticCipher{token: "gh-token"}, gh,
		business.ModulePrincipalRegistry{blobStreamCallerID: {Queues: []string{"datasource"}, CrossTenant: true}})
	installModuleWorkContextAuthority(t)
	ctx := stampModuleWorkContext(t, blobStreamCallerID, blobStreamOrgID)

	stream := &recordingFilesStream{}
	err := streamDatasourceFiles(ctx, &gen.FetchDatasourceFilesRequest{
		SourceId: blobStreamSourceID, Version: filesVersion,
		Files: []*gen.DatasourceFileRef{{Path: "docs/empty.md", ItemId: filesEmptyID}, {Path: "docs/big.md", ItemId: filesBigID}},
	}, stream)
	if err != nil {
		t.Fatal(err)
	}
	type file struct {
		header *gen.DatasourceFileHeader
		data   []byte
	}
	var files []file
	for _, f := range stream.frames {
		switch frame := f.GetFrame().(type) {
		case *gen.FetchDatasourceFilesFrame_Header:
			files = append(files, file{header: frame.Header})
		case *gen.FetchDatasourceFilesFrame_Data:
			if len(files) == 0 {
				t.Fatal("data before any header")
			}
			if len(frame.Data) > datasourceBlobChunkBytes {
				t.Fatalf("frame of %d bytes, past the %d bound", len(frame.Data), datasourceBlobChunkBytes)
			}
			files[len(files)-1].data = append(files[len(files)-1].data, frame.Data...)
		}
	}
	if len(files) != 2 || files[0].header.GetPath() != "docs/empty.md" || files[1].header.GetPath() != "docs/big.md" {
		t.Fatalf("files = %d, want empty then big in request order", len(files))
	}
	if len(files[0].data) != 0 || files[0].header.GetSize() != 0 {
		t.Fatal("the empty file carried data")
	}
	if !bytes.Equal(files[1].data, big) || files[1].header.GetSize() != int64(len(big)) {
		t.Fatalf("big file reassembled to %d bytes, want %d", len(files[1].data), len(big))
	}
	p := files[1].header.GetProvenance()
	if p.GetSourceId() != blobStreamSourceID || p.GetOrgId() != blobStreamRepoOrg || p.GetBoundaryNodeId() != "boundary-1" ||
		p.GetVersion() != filesVersion || p.GetItemId() != filesBigID {
		t.Fatalf("provenance = %v", p)
	}
	readers := files[1].header.GetReaders()
	if readers.GetBasis() != gen.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED || !readers.GetBoundaryReaders() {
		t.Fatalf("readers = %v, want source-scoped to the boundary", readers)
	}
}

// A typed refusal keeps its ErrorInfo and RetryInfo across the Connect
// translation, so a Connect caller can read the reason and the reset.
func TestTranslateGRPCErrorKeepsTypedDetails(t *testing.T) {
	st, err := status.New(codes.ResourceExhausted, "limited").WithDetails(
		&errdetails.ErrorInfo{Reason: "DATASOURCE_RATE_LIMITED", Domain: "saas.accounts.v1", Metadata: map[string]string{"reset_at": "2026-09-26T18:00:00Z"}},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(90e9)},
	)
	if err != nil {
		t.Fatal(err)
	}
	var ce *connect.Error
	if !errors.As(translateGRPCError(st.Err()), &ce) || ce.Code() != connect.CodeResourceExhausted {
		t.Fatalf("translated = %v", ce)
	}
	var reason string
	var retry bool
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			t.Fatal(err)
		}
		switch v := msg.(type) {
		case *errdetails.ErrorInfo:
			reason = v.GetReason() + "@" + v.GetMetadata()["reset_at"]
		case *errdetails.RetryInfo:
			retry = v.GetRetryDelay().AsDuration() == 90e9
		}
	}
	if reason != "DATASOURCE_RATE_LIMITED@2026-09-26T18:00:00Z" || !retry {
		t.Fatalf("details = %q retry=%v", reason, retry)
	}
}
