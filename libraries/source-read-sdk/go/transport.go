package accounts

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
)

type internalGateway struct {
	url    string
	client *http.Client
}

func (g internalGateway) BaseURL() string          { return g.url }
func (g internalGateway) HTTPClient() *http.Client { return g.client }

// NewInternal uses HTTP/2 gRPC on Accounts' private endpoint. HTTP endpoints use
// h2c inside the mesh; HTTPS endpoints retain normal certificate verification.
// Supply credentials through Connect interceptors, never through the URL.
func NewInternal(baseURL string, opts ...connect.ClientOption) (*Client, error) {
	gw, err := InternalGateway(baseURL)
	if err != nil {
		return nil, err
	}
	return New(gw, append(opts, connect.WithGRPC())...), nil
}

// InternalGateway is the Gateway of Accounts' private endpoint: HTTP/2, h2c for
// http:// inside the mesh, certificate-verified TLS for https://. The facade
// skips streaming RPCs, so a caller of FetchDatasourceBlob or
// FetchDatasourceFiles builds the generated accountsv1connect client on it
// (with connect.WithGRPC()) rather than dialling the endpoint itself.
func InternalGateway(baseURL string) (Gateway, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
		return nil, &url.Error{Op: "parse", URL: baseURL, Err: errInvalidInternalEndpoint{}}
	}
	transport := &http2.Transport{}
	if endpoint.Scheme == "http" {
		transport.AllowHTTP = true
		transport.DialTLSContext = func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
	}
	return internalGateway{url: baseURL, client: &http.Client{Transport: transport}}, nil
}

type errInvalidInternalEndpoint struct{}

func (errInvalidInternalEndpoint) Error() string {
	return "internal endpoint must be an HTTP(S) URL without user credentials"
}
