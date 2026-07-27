package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type fixedJWKSProvider struct {
	document string
	err      error
}

func (p fixedJWKSProvider) GetJWKS(context.Context) (string, error) {
	return p.document, p.err
}

func TestJWKSHTTPHandlerReturnsStandardDocument(t *testing.T) {
	handler := NewJWKSHTTPHandler(fixedJWKSProvider{
		document: `{"keys":[{"kty":"OKP","kid":"current"}]}`,
	})
	request := httptest.NewRequest(http.MethodGet, jwksPath, nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"keys":[{"kty":"OKP","kid":"current"}]}`, response.Body.String())
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	require.Contains(t, response.Header().Get("Cache-Control"), "must-revalidate")
}

func TestJWKSHTTPHandlerFailsClosed(t *testing.T) {
	tests := map[string]fixedJWKSProvider{
		"provider error": {err: errors.New("offline")},
		"invalid JSON":   {document: `not-json`},
		"empty keys":     {document: `{"keys":[]}`},
	}
	for name, provider := range tests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, jwksPath, nil)
			response := httptest.NewRecorder()

			NewJWKSHTTPHandler(provider).ServeHTTP(response, request)

			require.Equal(t, http.StatusServiceUnavailable, response.Code)
		})
	}
}

func TestJWKSHTTPHandlerRejectsWrongMethodAndPath(t *testing.T) {
	handler := NewJWKSHTTPHandler(fixedJWKSProvider{
		document: `{"keys":[{"kty":"OKP","kid":"current"}]}`,
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, jwksPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
	require.Equal(t, http.MethodGet, response.Header().Get("Allow"))

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/auth/not-jwks", nil))
	require.Equal(t, http.StatusNotFound, response.Code)
}
