package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The refresh cookie the Connect handler sets crosses the REST transcoder as
// header metadata. Through the matcher the REST mux is built with, it must
// reach the browser as Set-Cookie — storable and HttpOnly — and never as
// Grpc-Metadata-Set-Cookie, which a browser does not store and page script can
// read. Before the matcher, every browser sign-in through REST lost its session
// on the next load.
func TestTheRefreshCookieCrossesTheRESTTranscoderAsSetCookie(t *testing.T) {
	cookie := refreshCookie("rt-value", refreshCookieMaxAgeSeconds).String()
	recorder := forwardThroughTranscoder(t, runtime.WithOutgoingHeaderMatcher(restOutgoingHeaderMatcher),
		metadata.Pairs("set-cookie", cookie, "x-ratelimit-limit", "60"))

	if got := recorder.Header().Values("Set-Cookie"); len(got) != 1 || got[0] != cookie {
		t.Fatalf("Set-Cookie = %q, want the refresh cookie %q", got, cookie)
	}
	for name := range recorder.Header() {
		if strings.EqualFold(name, runtime.MetadataHeaderPrefix+"set-cookie") {
			t.Fatalf("the refresh cookie also leaked as %s, readable by page script", name)
		}
	}
	// Every other header keeps grpc-gateway's default spelling.
	if recorder.Header().Get(runtime.MetadataHeaderPrefix+"x-ratelimit-limit") != "60" {
		t.Fatalf("other metadata lost its default prefix: %v", recorder.Header())
	}
}

// The failure this guards, shown on the default matcher: the cookie arrives
// prefixed, and no Set-Cookie is written at all.
func TestTheDefaultTranscoderPrefixesTheCookie(t *testing.T) {
	recorder := forwardThroughTranscoder(t, nil,
		metadata.Pairs("set-cookie", refreshCookie("rt-value", 60).String()))
	if len(recorder.Header().Values("Set-Cookie")) != 0 ||
		recorder.Header().Get(runtime.MetadataHeaderPrefix+"set-cookie") == "" {
		t.Fatalf("expected the default transcoder to prefix the cookie, got %v", recorder.Header())
	}
}

func forwardThroughTranscoder(t *testing.T, option runtime.ServeMuxOption, header metadata.MD) *httptest.ResponseRecorder {
	t.Helper()
	var options []runtime.ServeMuxOption
	if option != nil {
		options = append(options, option)
	}
	mux := runtime.NewServeMux(options...)
	ctx := runtime.NewServerMetadataContext(context.Background(), runtime.ServerMetadata{HeaderMD: header})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/authenticate", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	_, marshaler := runtime.MarshalerForRequest(mux, request)
	runtime.ForwardResponseMessage(ctx, mux, marshaler, recorder, request, &emptypb.Empty{})
	return recorder
}

// rest_gen.go is generated; its CUSTOM lines survive regeneration only by
// being preserved. A regeneration that drops the matcher would silently bring
// the prefixed cookie back, so the wiring itself is held here.
func TestTheRESTMuxIsBuiltWithTheOutgoingHeaderMatcher(t *testing.T) {
	source, err := os.ReadFile("rest_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "runtime.WithOutgoingHeaderMatcher(restOutgoingHeaderMatcher)") {
		t.Fatal("rest_gen.go no longer builds the REST mux with restOutgoingHeaderMatcher: the refresh cookie would leave as Grpc-Metadata-Set-Cookie")
	}
}
