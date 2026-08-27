package githubconnector_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/githubconnector"
)

const installationTokenValue = "ghs_installation_token"

// fakeGitHub emulates the two REST endpoints the connector touches: minting an
// installation token and reading repo contents. It verifies the app JWT with
// the app's public key and counts how many tokens it minted.
type fakeGitHub struct {
	appPublicKey *rsa.PublicKey
	appID        string
	tokenExpiry  time.Time
	files        map[string]githubFile
	mintDelay    time.Duration

	mu              sync.Mutex
	mintCount       int
	lastContentPath string
}

type githubFile struct {
	content  []byte
	dir      []map[string]any
	tooLarge bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/access_tokens"):
		f.handleMint(w, r)
	case strings.Contains(r.URL.Path, "/contents"):
		f.handleContents(w, r)
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (f *fakeGitHub) handleMint(w http.ResponseWriter, r *http.Request) {
	rawJWT := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims := jwt.RegisteredClaims{}
	// Verify the signature, method, and issuer. Time claims are driven by the
	// connector's (possibly synthetic) clock in these tests, so skip validating
	// them against the server's real wall-clock.
	_, err := jwt.ParseWithClaims(rawJWT, &claims, func(*jwt.Token) (any, error) {
		return f.appPublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	if err != nil || claims.Issuer != f.appID {
		http.Error(w, `{"message":"bad app jwt"}`, http.StatusUnauthorized)
		return
	}

	if f.mintDelay > 0 {
		time.Sleep(f.mintDelay)
	}

	f.mu.Lock()
	f.mintCount++
	f.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      installationTokenValue,
		"expires_at": f.tokenExpiry.Format(time.RFC3339),
	})
}

func (f *fakeGitHub) handleContents(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+installationTokenValue {
		http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.lastContentPath = r.URL.Path
	f.mu.Unlock()
	_, path, _ := strings.Cut(r.URL.Path, "/contents")
	path = strings.TrimPrefix(path, "/")
	file, ok := f.files[path]
	if !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	if file.dir != nil {
		writeJSON(w, http.StatusOK, file.dir)
		return
	}
	if file.tooLarge {
		// GitHub's contents API response for a file over its size cap.
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "file", "name": path, "path": path, "sha": "deadbeef",
			"size": 2_000_000, "encoding": "none", "content": "",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type":     "file",
		"name":     path,
		"path":     path,
		"sha":      "deadbeef",
		"size":     len(file.content),
		"encoding": "base64",
		"content":  wrapBase64(file.content),
	})
}

func (f *fakeGitHub) mints() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mintCount
}

func (f *fakeGitHub) contentPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastContentPath
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// wrapBase64 mimics GitHub's column-60 line wrapping of the base64 payload.
func wrapBase64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(encoded); i += 60 {
		end := i + 60
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(encoded[i:end])
		b.WriteString("\n")
	}
	return b.String()
}

func credentialWithKey(t *testing.T) (githubconnector.AppCredential, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	cred := githubconnector.AppCredential{AppID: "123456", InstallationID: "789", PrivateKeyPEM: string(keyPEM)}
	return cred, key
}

func TestMintInstallationTokenSignsValidAppJWT(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{appPublicKey: &key.PublicKey, appID: cred.AppID, tokenExpiry: time.Now().Add(time.Hour)}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	token, err := conn.MintInstallationToken(context.Background(), cred)
	require.NoError(t, err)
	require.Equal(t, installationTokenValue, token.Token)
	require.WithinDuration(t, fake.tokenExpiry, token.ExpiresAt, time.Second)
	require.Equal(t, 1, fake.mints())
}

func TestMintInstallationTokenSurfacesAPIError(t *testing.T) {
	cred, _ := credentialWithKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	_, err := conn.MintInstallationToken(context.Background(), cred)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Bad credentials")
}

func TestFetchRepoContentsDecodesFile(t *testing.T) {
	cred, key := credentialWithKey(t)
	want := []byte("# Living Wiki\n\nHello from GitHub.\n")
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  time.Now().Add(time.Hour),
		files:        map[string]githubFile{"docs/README.md": {content: want}},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	got, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "docs/README.md", "main")
	require.NoError(t, err)
	require.Equal(t, githubconnector.ContentTypeFile, got.Type)
	require.Equal(t, want, got.Content)
	require.Equal(t, "deadbeef", got.SHA)
}

func TestFetchRepoContentsListsDirectory(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  time.Now().Add(time.Hour),
		files: map[string]githubFile{"docs": {dir: []map[string]any{
			{"type": "file", "name": "README.md", "path": "docs/README.md", "sha": "aaa", "size": 10},
			{"type": "dir", "name": "guides", "path": "docs/guides", "sha": "bbb", "size": 0},
		}}},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	got, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "docs", "")
	require.NoError(t, err)
	require.Equal(t, githubconnector.ContentTypeDir, got.Type)
	require.Len(t, got.Entries, 2)
	require.Equal(t, "docs/README.md", got.Entries[0].Path)
	require.Equal(t, githubconnector.ContentTypeDir, got.Entries[1].Type)
	require.Nil(t, got.Entries[0].Content)
}

func TestFetchRepoContentsRootUsesBareEndpoint(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  time.Now().Add(time.Hour),
		files: map[string]githubFile{"": {dir: []map[string]any{
			{"type": "file", "name": "README.md", "path": "README.md", "sha": "aaa", "size": 4},
		}}},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	got, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "", "")
	require.NoError(t, err)
	require.Equal(t, githubconnector.ContentTypeDir, got.Type)
	require.Len(t, got.Entries, 1)
	// The repo root must hit the canonical bare endpoint, not a trailing-slash form.
	require.Equal(t, "/repos/acme/wiki/contents", fake.contentPath())
}

func TestFetchRepoContentsTooLargeFileIsDistinguishable(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  time.Now().Add(time.Hour),
		files:        map[string]githubFile{"big.bin": {tooLarge: true}},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	_, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "big.bin", "")
	require.ErrorIs(t, err, githubconnector.ErrFileTooLarge,
		"an oversized file must be a distinguishable sentinel so callers can skip it")
}

func TestFetchRepoContentsNotFound(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{appPublicKey: &key.PublicKey, appID: cred.AppID, tokenExpiry: time.Now().Add(time.Hour), files: map[string]githubFile{}}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))
	_, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "missing.md", "")
	require.Error(t, err)
	require.True(t, githubconnector.IsNotFound(err), "expected a 404 classification, got %v", err)
}

func TestFetchRepoContentsCachesInstallationToken(t *testing.T) {
	cred, key := credentialWithKey(t)
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  base.Add(time.Hour),
		files:        map[string]githubFile{"a.md": {content: []byte("a")}, "b.md": {content: []byte("b")}},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	now := base
	conn := githubconnector.NewConnector(
		githubconnector.WithBaseURL(server.URL),
		githubconnector.WithClock(func() time.Time { return now }),
	)

	for _, path := range []string{"a.md", "b.md"} {
		_, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", path, "")
		require.NoError(t, err)
	}
	require.Equal(t, 1, fake.mints(), "second fetch must reuse the cached token")

	// Advance past the refresh window (expiry - 1m); the next fetch re-mints.
	now = base.Add(60 * time.Minute)
	_, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "a.md", "")
	require.NoError(t, err)
	require.Equal(t, 2, fake.mints(), "an expiring token must be re-minted")
}

func TestFetchRepoContentsSingleFlightsTokenMint(t *testing.T) {
	cred, key := credentialWithKey(t)
	fake := &fakeGitHub{
		appPublicKey: &key.PublicKey,
		appID:        cred.AppID,
		tokenExpiry:  time.Now().Add(time.Hour),
		files:        map[string]githubFile{"a.md": {content: []byte("a")}},
		// Widen the window so all goroutines miss the cache and pile up behind the
		// single in-flight mint.
		mintDelay: 30 * time.Millisecond,
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	conn := githubconnector.NewConnector(githubconnector.WithBaseURL(server.URL))

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := conn.FetchRepoContents(context.Background(), cred, "acme", "wiki", "a.md", "")
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	require.Equal(t, 1, fake.mints(), "concurrent first fetches must share a single installation-token mint")
}
