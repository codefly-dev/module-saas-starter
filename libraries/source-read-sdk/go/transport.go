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
	return New(internalGateway{url: baseURL, client: &http.Client{Transport: transport}}, opts...), nil
}

type errInvalidInternalEndpoint struct{}

func (errInvalidInternalEndpoint) Error() string {
	return "internal endpoint must be an HTTP(S) URL without user credentials"
}
