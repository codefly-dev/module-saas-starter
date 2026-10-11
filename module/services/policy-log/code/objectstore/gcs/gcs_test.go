package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"policy-log/objectstore"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func fakeGCS(t *testing.T, transport transportFunc) *GCS {
	t.Helper()
	client, err := storage.NewClient(context.Background(), option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithoutAuthentication(), storage.WithJSONReads(), storage.WithDisabledClientMetrics())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return newGCS(client, "example-policy-log")
}

func TestGCSWritesUseExactGenerationPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation int64
		want       string
	}{{objectstore.HeadObject, 41, "41"}, {"entries/00000000000000000001.json", 0, "0"}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := fakeGCS(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Query().Get("ifGenerationMatch") != tc.want || r.URL.Query().Get("uploadType") != "multipart" {
					t.Errorf("conditional upload = %s %s", r.Method, r.URL)
				}
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if !strings.Contains(string(b), `"crc32c"`) || !strings.Contains(string(b), `"cacheControl":"no-store"`) || !strings.Contains(string(b), `{"seq":1}`) {
					t.Errorf("upload omitted checksum, cache policy or bytes: %s", b)
				}
				return response(200, `{"generation":"42"}`), nil
			})
			generation, err := s.Write(context.Background(), tc.name, []byte(`{"seq":1}`), tc.generation)
			if err != nil || generation != 42 || calls.Load() != 1 {
				t.Fatalf("Write: generation=%d calls=%d err=%v", generation, calls.Load(), err)
			}
		})
	}
}

func TestGCSLostPreconditionAndServerFailureNeverRetry(t *testing.T) {
	for _, code := range []int{412, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			s := fakeGCS(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				return response(code, fmt.Sprintf(`{"error":{"code":%d,"message":"refused"}}`, code)), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			generation, err := s.Write(ctx, objectstore.HeadObject, []byte(`{}`), 7)
			if err == nil || generation != 0 || calls.Load() != 1 {
				t.Fatalf("Write retried or succeeded: generation=%d calls=%d err=%v", generation, calls.Load(), err)
			}
			if code == 412 && !errors.Is(err, objectstore.ErrPrecondition) {
				t.Fatalf("lost generation was not classified: %v", err)
			}
			var apiError *googleapi.Error
			if code == 503 && (!errors.As(err, &apiError) || apiError.Code != 503) {
				t.Fatalf("server failure retried until deadline: %v", err)
			}
		})
	}
}

func TestGCSReadReturnsBytesAndGenerationFromOneResponse(t *testing.T) {
	var calls atomic.Int32
	s := fakeGCS(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Query().Get("alt") != "media" {
			t.Errorf("not one object read: %s %s", r.Method, r.URL)
		}
		rsp := response(200, `{"seq":1}`)
		rsp.Header.Set("X-Goog-Generation", "19")
		rsp.Header.Set("Content-Type", "application/json")
		rsp.Header.Set("Content-Length", fmt.Sprint(rsp.ContentLength))
		return rsp, nil
	})
	obj, err := s.Read(context.Background(), objectstore.HeadObject)
	if err != nil || obj.Generation != 19 || string(obj.Data) != `{"seq":1}` || calls.Load() != 1 {
		t.Fatalf("Read: %+v calls=%d err=%v", obj, calls.Load(), err)
	}
}

func TestGCSRefusesUnconditionalHeadAndEntryOverwrite(t *testing.T) {
	s := fakeGCS(t, func(*http.Request) (*http.Response, error) {
		t.Error("invalid write reached GCS")
		return nil, errors.New("unexpected")
	})
	for _, tc := range []struct {
		name       string
		generation int64
	}{{objectstore.HeadObject, 0}, {objectstore.HeadObject, -1}, {"entries/00000000000000000001.json", 9}, {"other.json", 0}, {"entries/../../head.json", 0}} {
		if _, err := s.Write(context.Background(), tc.name, []byte(`{}`), tc.generation); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Write(ctx, objectstore.HeadObject, []byte(`{}`), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if _, err := s.Read(ctx, objectstore.HeadObject); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestGCSRejectsMissingOversizedAndGenerationlessObjects(t *testing.T) {
	for _, tc := range []struct {
		name             string
		code             int
		body, generation string
	}{{"missing", 404, `{"error":{"code":404}}`, ""}, {"oversized", 200, strings.Repeat("x", objectstore.MaxObjectBytes+1), "2"}, {"generationless", 200, `{}`, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeGCS(t, func(*http.Request) (*http.Response, error) {
				r := response(tc.code, tc.body)
				r.Header.Set("Content-Length", fmt.Sprint(r.ContentLength))
				r.Header.Set("X-Goog-Generation", tc.generation)
				return r, nil
			})
			_, err := s.Read(context.Background(), objectstore.HeadObject)
			if err == nil {
				t.Fatal("invalid read accepted")
			}
			if tc.code == 404 && !errors.Is(err, objectstore.ErrNotFound) {
				t.Fatalf("missing classification: %v", err)
			}
		})
	}
}

func TestGCSBootRefusesEmulatorOverride(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "http://example.invalid")
	if s, err := New(context.Background(), "example-policy-log"); s != nil || err == nil {
		t.Fatal("emulator override accepted")
	}
}
