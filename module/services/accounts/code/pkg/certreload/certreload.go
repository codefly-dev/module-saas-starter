// Package certreload serves a rotated TLS leaf without a process restart. Every
// custody listener snapshots its leaf at startup today, so a cert-manager
// rotation of the 24h workload leaves is not picked up until the pod is
// recreated; between the rotation and the restart the process keeps presenting
// the stale leaf and, once it expires, every handshake fails. A Reloader re-reads
// the mounted certificate/key files when they change and serves the new leaf on
// the next handshake via tls.Config.GetCertificate.
package certreload

import (
	"crypto/tls"
	"os"
	"sync"
	"time"
)

// Read returns the bytes of a mounted file. The custody host passes its
// permission-checking projected-file reader so a rotation is re-validated on
// every reload, exactly as it is at startup.
type Read func(string) ([]byte, error)

type Reloader struct {
	certFile, keyFile string
	read              Read

	mu      sync.RWMutex
	current *tls.Certificate
	certMod time.Time
	keyMod  time.Time

	checkMu sync.Mutex
}

// New loads the initial pair once. A startup failure is fatal to the caller: a
// listener must never come up without a valid leaf.
func New(certFile, keyFile string, read Read) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile, read: read}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reloader) load() error {
	certPEM, err := r.read(r.certFile)
	if err != nil {
		return err
	}
	keyPEM, err := r.read(r.keyFile)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.current = &pair
	r.certMod = certInfo.ModTime()
	r.keyMod = keyInfo.ModTime()
	r.mu.Unlock()
	return nil
}

// refresh re-reads the pair only when a file's modification time advanced past
// the loaded copy. Stat follows the Kubernetes atomic projected-secret symlink,
// so a rotation is observed. Any stat, read, permission or parse failure is
// swallowed so the last good pair keeps serving; a half-written replacement is
// picked up on a later handshake once both files settle.
func (r *Reloader) refresh() {
	r.checkMu.Lock()
	defer r.checkMu.Unlock()
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return
	}
	r.mu.RLock()
	changed := certInfo.ModTime().After(r.certMod) || keyInfo.ModTime().After(r.keyMod)
	r.mu.RUnlock()
	if changed {
		_ = r.load()
	}
}

// Current returns the leaf a handshake would serve now, refreshing first.
func (r *Reloader) Current() *tls.Certificate {
	r.refresh()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

// GetCertificate is a tls.Config.GetCertificate callback for the custody and
// revision listeners: it serves the current leaf to every inbound handshake.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.Current(), nil
}
