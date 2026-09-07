package adapters

import (
	"bytes"
	"errors"
	"testing"

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
