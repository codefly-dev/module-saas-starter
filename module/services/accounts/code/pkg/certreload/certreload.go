// Package certreload serves rotated TLS material without a process restart.
// Every custody listener snapshots its leaf and client CA bundle at startup
// today, so a cert-manager rotation of the 24h workload leaves is not picked up
// until the pod is recreated; between the rotation and the restart the process
// keeps presenting the stale leaf and, once it expires, every handshake fails.
// A re-issued client CA fails the same way from the other side: workers holding
// leaves from the new CA are refused until a restart.
//
// A Reloader re-reads the mounted certificate, key and client CA files through
// the caller's reader and serves the new material on the next handshake via
// tls.Config.GetCertificate and tls.Config.GetConfigForClient.
//
// Change detection compares the file contents actually read against the
// contents the live material was built from. It deliberately does not consult
// modification times: a stat is a second observation of a file that may have
// been replaced since it was read, so recording a stat taken after a read can
// pin a new file's timestamp against the old file's bytes and suppress every
// later reload. Comparing bytes has no such window — the material is rebuilt
// from exactly the bytes that were compared.
package certreload

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync"
	"time"
)

// Read returns the bytes of a mounted file. The custody host passes its
// permission-checking projected-file reader, so every reload re-validates the
// replacement exactly as startup validates the original. The Reloader performs
// no filesystem access of its own: this is the only way it observes a file.
type Read func(string) ([]byte, error)

// CheckInterval bounds how often inbound handshakes re-read the mounted files.
// Without it a handshake burst — or a persistently malformed replacement, which
// never updates the loaded copy and so never stops looking changed — turns every
// connection into three file reads behind a single mutex. It is exported so a
// caller's tests can wait exactly one check rather than guess at a delay.
const CheckInterval = time.Second

type Reloader struct {
	certFile, keyFile, clientCAFile string
	read                            Read

	mu      sync.RWMutex
	current *tls.Certificate
	pool    *x509.CertPool
	// certPEM, keyPEM and caPEM are the exact bytes current and pool were built
	// from, and are the comparison basis for detecting a rotation.
	certPEM, keyPEM, caPEM []byte

	checkMu   sync.Mutex
	lastCheck time.Time
	now       func() time.Time
	interval  time.Duration
	// observe, when set, is told the outcome of every reload that changed or
	// tried to change the material. lastReport suppresses an unchanged outcome so
	// a standing failure is reported once rather than once per check.
	observe    func(error)
	lastReport string
	reported   bool
}

// Observe registers a callback told the outcome of each reload that changed, or
// tried to change, the served material: nil on a successful swap, the refusal
// otherwise. A refused rotation is otherwise indistinguishable from one that
// never happened, and only becomes visible hours later when the old leaf
// expires. Repeats of the same outcome are suppressed. The callback runs on a
// handshake goroutine while an internal lock is held, so it must not block or
// re-enter the Reloader. Call before serving begins.
func (r *Reloader) Observe(f func(error)) { r.observe = f }

func (r *Reloader) report(err error) {
	if r.observe == nil {
		return
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	if r.reported && text == r.lastReport {
		return
	}
	r.reported, r.lastReport = true, text
	r.observe(err)
}

// New loads the initial material once. A startup failure is fatal to the
// caller: a listener must never come up without a valid leaf and client CA.
func New(certFile, keyFile, clientCAFile string, read Read) (*Reloader, error) {
	if read == nil {
		return nil, errors.New("custody projection reader required")
	}
	r := &Reloader{certFile: certFile, keyFile: keyFile, clientCAFile: clientCAFile, read: read, now: time.Now, interval: CheckInterval}
	if _, err := r.load(); err != nil {
		return nil, err
	}
	if r.current == nil || r.pool == nil {
		return nil, errors.New("empty custody TLS material")
	}
	return r, nil
}

// load reads all three files, and rebuilds the served material only when the
// bytes differ from those the live material was built from. It is the only
// writer of current/pool and records the bytes it actually parsed, so a rotation
// that lands mid-read either produces a mismatched pair — rejected here, retried
// on the next check because the loaded copy is left untouched — or a coherent
// newer pair, which is correct to serve.
func (r *Reloader) load() (changed bool, err error) {
	certPEM, err := r.read(r.certFile)
	if err != nil {
		return false, err
	}
	keyPEM, err := r.read(r.keyFile)
	if err != nil {
		return false, err
	}
	caPEM, err := r.read(r.clientCAFile)
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	unchanged := r.current != nil && r.pool != nil &&
		bytes.Equal(certPEM, r.certPEM) && bytes.Equal(keyPEM, r.keyPEM) && bytes.Equal(caPEM, r.caPEM)
	r.mu.RUnlock()
	if unchanged {
		return false, nil
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return false, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return false, errors.New("invalid custody client CA")
	}
	r.mu.Lock()
	r.current, r.pool = &pair, pool
	r.certPEM, r.keyPEM, r.caPEM = certPEM, keyPEM, caPEM
	r.mu.Unlock()
	return true, nil
}

// refresh re-reads the mounted files at most once per interval. Any read,
// permission or parse failure is swallowed so the last good material keeps
// serving; a half-written replacement is picked up on a later handshake once
// the files settle.
func (r *Reloader) refresh() {
	r.checkMu.Lock()
	defer r.checkMu.Unlock()
	now := r.now()
	if !r.lastCheck.IsZero() && now.Sub(r.lastCheck) < r.interval {
		return
	}
	r.lastCheck = now
	changed, err := r.load()
	if changed || err != nil {
		r.report(err)
	}
}

// GetCertificate is a tls.Config.GetCertificate callback for the custody and
// revision listeners. Both serve a single workload identity, so the leaf is
// chosen without consulting the ClientHello; a listener that ever needs to pick
// between identities must select on hello.SupportsCertificate instead.
//
// The owning tls.Config must leave Certificates empty. crypto/tls only consults
// GetCertificate when Certificates is empty or the ClientHello carries SNI, so a
// startup snapshot left in Certificates silently pins the leaf for every client
// that dials by IP address and sends no SNI.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current, nil
}

// GetConfigForClient is a tls.Config.GetConfigForClient callback that carries a
// rotated client CA bundle onto a live listener. It returns trust material only;
// the serving adapter re-applies its own hardening to whatever comes back, so
// this can never relax client auth, minimum version or verification.
func (r *Reloader) GetConfigForClient(*tls.ClientHelloInfo) (*tls.Config, error) {
	r.refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return &tls.Config{GetCertificate: r.GetCertificate, ClientCAs: r.pool, MinVersion: tls.VersionTLS13}, nil
}

// ClientCAs returns the client CA pool loaded at startup, for seeding the
// listener's tls.Config. Rotations reach the listener through
// GetConfigForClient; this accessor does not re-read the mount.
func (r *Reloader) ClientCAs() *x509.CertPool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pool
}
