package keyservice

// Fakes for the two key services, used by the conformance suite.
//
// Each fake does the real cryptography the backend depends on — AES-GCM with
// the associated data actually bound, HMAC-SHA256, real Ed25519 — because the
// properties the suite asserts are properties of that binding. A fake that
// ignored the associated data would let "a payload moved between rows no longer
// opens" pass while production had lost it.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeVault serves the three Transit endpoints accounts uses, plus the health
// probe. Its ciphertexts carry Vault's own `vault:v<N>:` key-version prefix, so
// the backend's handling of that prefix is exercised rather than assumed.
type fakeVault struct {
	server  *httptest.Server
	aead    cipher.AEAD
	macKey  []byte
	version int
	// token is the only token the fake accepts, so a request that lost its
	// header is a 403 rather than a pass.
	token string
	// outage answers every Transit call with this status, for the tests that
	// separate "the key service is down" from "this credential is broken".
	outage int
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	v := &fakeVault{aead: newTestAEAD(t), macKey: randomBytes(t, 32), version: 1, token: "fake-vault-token"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sys/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/transit/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != v.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if v.outage != 0 {
			w.WriteHeader(v.outage)
			_, _ = w.Write([]byte("sensitive-provider-body"))
			return
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		operation := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/transit/"), "/")[0]
		switch operation {
		case "encrypt":
			plaintext, err := base64.StdEncoding.DecodeString(request["plaintext"])
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeVaultData(w, map[string]any{"ciphertext": v.seal(plaintext)})
		case "decrypt":
			plaintext, ok := v.open(request["ciphertext"])
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeVaultData(w, map[string]any{"plaintext": base64.StdEncoding.EncodeToString(plaintext)})
		case "hmac":
			input, err := base64.StdEncoding.DecodeString(request["input"])
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mac := hmac.New(sha256.New, v.macKey)
			mac.Write(input)
			writeVaultData(w, map[string]any{
				"hmac": v.prefix() + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	v.server = httptest.NewServer(mux)
	t.Cleanup(v.server.Close)
	return v
}

func (v *fakeVault) prefix() string { return "vault:v" + strconv.Itoa(v.version) + ":" }

func (v *fakeVault) seal(plaintext []byte) string {
	nonce := randomBytesN(v.aead.NonceSize())
	sealed := v.aead.Seal(nonce, nonce, plaintext, nil)
	return v.prefix() + base64.StdEncoding.EncodeToString(sealed)
}

func (v *fakeVault) open(ciphertext string) ([]byte, bool) {
	if !strings.HasPrefix(ciphertext, "vault:v") {
		return nil, false
	}
	_, encoded, found := strings.Cut(strings.TrimPrefix(ciphertext, "vault:v"), ":")
	if !found {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < v.aead.NonceSize() {
		return nil, false
	}
	nonce, body := raw[:v.aead.NonceSize()], raw[v.aead.NonceSize():]
	plaintext, err := v.aead.Open(nil, nonce, body, nil)
	return plaintext, err == nil
}

func (v *fakeVault) sealer() *VaultSealer {
	sealer := NewVaultSealerDirect(v.server.URL, v.token)
	sealer.client = v.server.Client()
	return sealer
}

func writeVaultData(w http.ResponseWriter, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// fakeKMS serves the Cloud KMS surface the backend uses, plus the metadata
// server that issues the workload identity's token. Keys are addressed by their
// resource name, so a request for a key the fake does not hold is the 404 a real
// project answers, and a key whose purpose or algorithm does not match the
// backend's requirement is served as configured so the refusals can be driven.
type fakeKMS struct {
	server   *httptest.Server
	keys     map[string]*fakeKMSKey
	token    string
	tokenTTL int
	// outage answers every KMS call with this status: 403 is what an
	// unprivileged workload identity meets, 503 is the service being down.
	outage int
	// metadataStatus overrides the metadata server's status, for a pod with no
	// bound service account.
	metadataStatus int
	// failOperations refuses the data-plane operations while still answering key
	// metadata — a role granted viewer but not encrypter/decrypter.
	failOperations bool
}

type fakeKMSKey struct {
	purpose   string
	algorithm string
	// versions are ordinal → state. The primary is the highest ENABLED one.
	versions map[int]string
	aead     cipher.AEAD
	macKey   []byte
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	k := &fakeKMS{keys: map[string]*fakeKMSKey{}, token: "fake-metadata-token", tokenTTL: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/computeMetadata/v1/instance/service-accounts/default/token",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if k.metadataStatus != 0 {
				w.WriteHeader(k.metadataStatus)
				return
			}
			writeJSON(w, map[string]any{"access_token": k.token, "expires_in": k.tokenTTL})
		})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+k.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if k.outage != 0 {
			w.WriteHeader(k.outage)
			_, _ = w.Write([]byte("sensitive-provider-body"))
			return
		}
		k.serve(w, r)
	})
	k.server = httptest.NewServer(mux)
	t.Cleanup(k.server.Close)
	return k
}

func (k *fakeKMS) config() KMSConfig {
	return KMSConfig{
		apiBase:      k.server.URL + "/v1/",
		metadataBase: k.server.URL + "/computeMetadata/v1/",
	}
}

// addSymmetricKey registers an ENCRYPT_DECRYPT key with one enabled version.
func (k *fakeKMS) addSymmetricKey(t *testing.T, name string) string {
	t.Helper()
	k.keys[name] = &fakeKMSKey{
		purpose: purposeEncryptDecrypt, algorithm: algorithmSymmetric,
		versions: map[int]string{1: versionStateEnabled}, aead: newTestAEAD(t),
	}
	return name
}

// addMACKey registers a MAC key and returns the resource name of version 1.
func (k *fakeKMS) addMACKey(t *testing.T, name string) string {
	t.Helper()
	k.keys[name] = &fakeKMSKey{
		purpose: purposeMAC, algorithm: algorithmHMACSHA256,
		versions: map[int]string{1: versionStateEnabled}, macKey: randomBytes(t, 32),
	}
	return name + "/cryptoKeyVersions/1"
}

func (k *fakeKMS) serve(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch {
	case strings.HasSuffix(resource, ":encrypt"):
		k.encrypt(w, r, strings.TrimSuffix(resource, ":encrypt"))
	case strings.HasSuffix(resource, ":decrypt"):
		k.decrypt(w, r, strings.TrimSuffix(resource, ":decrypt"))
	case strings.HasSuffix(resource, ":macSign"):
		k.macSign(w, r, strings.TrimSuffix(resource, ":macSign"))
	default:
		k.describe(w, resource)
	}
}

func (k *fakeKMS) describe(w http.ResponseWriter, resource string) {
	if name, ordinal, ok := splitVersion(resource); ok {
		key := k.keys[name]
		if key == nil || key.versions[ordinal] == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, kmsCryptoKeyVersion{Name: resource, State: key.versions[ordinal], Algorithm: key.algorithm})
		return
	}
	key := k.keys[resource]
	if key == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	described := map[string]any{
		"purpose":         key.purpose,
		"versionTemplate": map[string]any{"algorithm": key.algorithm},
	}
	if key.purpose == purposeEncryptDecrypt {
		described["primary"] = map[string]any{
			"name":  resource + "/cryptoKeyVersions/" + strconv.Itoa(key.primary()),
			"state": versionStateEnabled,
		}
	}
	writeJSON(w, described)
}

func (k *fakeKMS) encrypt(w http.ResponseWriter, r *http.Request, name string) {
	if k.failOperations {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	key := k.keys[name]
	if key == nil || key.aead == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	request := decodeJSON(w, r)
	if request == nil {
		return
	}
	plaintext, err := base64.StdEncoding.DecodeString(request["plaintext"])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	aad, err := base64.StdEncoding.DecodeString(request["additionalAuthenticatedData"])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nonce := randomBytesN(key.aead.NonceSize())
	sealed := key.aead.Seal(nonce, nonce, plaintext, aad)
	writeJSON(w, map[string]any{
		"name":       name + "/cryptoKeyVersions/" + strconv.Itoa(key.primary()),
		"ciphertext": base64.StdEncoding.EncodeToString(sealed),
	})
}

func (k *fakeKMS) decrypt(w http.ResponseWriter, r *http.Request, name string) {
	key := k.keys[name]
	if key == nil || key.aead == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	request := decodeJSON(w, r)
	if request == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(request["ciphertext"])
	if err != nil || len(raw) < key.aead.NonceSize() {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	aad, err := base64.StdEncoding.DecodeString(request["additionalAuthenticatedData"])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nonce, body := raw[:key.aead.NonceSize()], raw[key.aead.NonceSize():]
	plaintext, err := key.aead.Open(nil, nonce, body, aad)
	if err != nil {
		// What Cloud KMS answers when the associated data does not match.
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"plaintext": base64.StdEncoding.EncodeToString(plaintext)})
}

func (k *fakeKMS) macSign(w http.ResponseWriter, r *http.Request, version string) {
	name, ordinal, ok := splitVersion(version)
	key := k.keys[name]
	if !ok || key == nil || key.macKey == nil || key.versions[ordinal] != versionStateEnabled {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	request := decodeJSON(w, r)
	if request == nil {
		return
	}
	data, err := base64.StdEncoding.DecodeString(request["data"])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mac := hmac.New(sha256.New, append(key.macKey, byte(ordinal)))
	mac.Write(data)
	writeJSON(w, map[string]any{"name": version, "mac": base64.StdEncoding.EncodeToString(mac.Sum(nil))})
}

func (key *fakeKMSKey) primary() int {
	primary := 0
	for ordinal, state := range key.versions {
		if state == versionStateEnabled && ordinal > primary {
			primary = ordinal
		}
	}
	return primary
}

func splitVersion(resource string) (string, int, bool) {
	name, ordinal, found := strings.Cut(resource, "/cryptoKeyVersions/")
	if !found {
		return "", 0, false
	}
	parsed, err := strconv.Atoi(ordinal)
	if err != nil {
		return "", 0, false
	}
	return name, parsed, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request) map[string]string {
	var request map[string]string
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	return request
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func newTestAEAD(t *testing.T) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(randomBytes(t, 32))
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return aead
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return buf
}

func randomBytesN(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("read random: %v", err))
	}
	return buf
}

// testKeyName builds a syntactically valid Cloud KMS resource name.
func testKeyName(key string) string {
	return "projects/acme-host/locations/europe-west1/keyRings/accounts/cryptoKeys/" + key
}
