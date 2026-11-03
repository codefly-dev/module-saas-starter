package adapters

import "net/http"

// unencryptedHTTP2 declares a test listener that speaks h2c — HTTP/2 with no
// TLS — which is what an in-cell listener behind the mesh is.
//
// golang.org/x/net/http2/h2c is deprecated and unmaintained, and its
// `NewHandler(handler, &http2.Server{})` wrapper is exactly what
// `http.Server.Protocols` with `SetUnencryptedHTTP2` now does in the standard
// library. The generated listeners (`*_gen.go`) still wrap with h2c; that is
// the generator's declaration to change, not this file's.
func unencryptedHTTP2() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return protocols
}
