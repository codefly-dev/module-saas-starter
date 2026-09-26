package github

import (
	"bytes"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func strconvItoa(v int64) string { return strconv.FormatInt(v, 10) }

// fakeGitHub is a local stand-in for github.com: git's own http-backend serves
// the smart-HTTP transport for repositories under root, and a small REST handler
// answers the repository visibility read. It counts every request by kind, so a
// test can hold a sync to a number of calls, and can be told to refuse git with
// a status or to require a credential.
type fakeGitHub struct {
	t      *testing.T
	root   string
	server *httptest.Server
	work   string

	requireAuth string // when set, git requests must carry this Authorization
	refuse      atomic.Int32

	uploadPacks atomic.Int64 // git-upload-pack POSTs: one per fetch or ls-remote
	gitRequests atomic.Int64 // every git HTTP request
	restCalls   atomic.Int64 // every /repos/ REST request

	mu          sync.Mutex
	authHeaders []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("these tests need git on PATH: %v", err)
	}
	f := &fakeGitHub{t: t, root: t.TempDir(), work: t.TempDir()}
	home := t.TempDir()
	backend := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + f.root,
			"GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + home,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=3",
			"GIT_CONFIG_KEY_0=uploadpack.allowFilter",
			"GIT_CONFIG_VALUE_0=true",
			"GIT_CONFIG_KEY_1=uploadpack.allowAnySHA1InWant",
			"GIT_CONFIG_VALUE_1=true",
			"GIT_CONFIG_KEY_2=protocol.version",
			"GIT_CONFIG_VALUE_2=2",
		},
		InheritEnv: []string{"PATH"},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			f.restCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"private":false,"visibility":"public"}`))
			return
		}
		f.gitRequests.Add(1)
		if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			f.uploadPacks.Add(1)
		}
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if code := f.refuse.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		if f.requireAuth != "" && r.Header.Get("Authorization") != f.requireAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) gitCmd(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Jane Doe", "GIT_AUTHOR_EMAIL=user@example.com",
		"GIT_COMMITTER_NAME=Jane Doe", "GIT_COMMITTER_EMAIL=user@example.com",
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// repo creates acme/<name> with a working copy the test commits through, and
// returns a function that commits the working copy's state (files maps a path
// to its content; a nil content deletes the path) and pushes it to main.
func (f *fakeGitHub) repo(name string) func(files map[string]*string) string {
	f.t.Helper()
	bare := filepath.Join(f.root, "acme", name+".git")
	f.gitCmd(f.root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	work := filepath.Join(f.work, name)
	f.gitCmd(f.work, "init", "--quiet", "--initial-branch=main", work)
	f.gitCmd(work, "remote", "add", "origin", bare)
	return func(files map[string]*string) string {
		f.t.Helper()
		for path, content := range files {
			full := filepath.Join(work, path)
			if content == nil {
				if err := os.Remove(full); err != nil {
					f.t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				f.t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(*content), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}
		f.gitCmd(work, "add", "-A")
		f.gitCmd(work, "commit", "--quiet", "--allow-empty", "-m", "change")
		f.gitCmd(work, "push", "--quiet", "--force", "origin", "HEAD:main")
		return f.gitCmd(work, "rev-parse", "HEAD")
	}
}

// resetTo force-moves main to an older commit of the working copy.
func (f *fakeGitHub) forcePush(name, commit string) {
	work := filepath.Join(f.work, name)
	f.gitCmd(work, "reset", "--quiet", "--hard", commit)
	f.gitCmd(work, "push", "--quiet", "--force", "origin", "HEAD:main")
}

// branchFrom commits content on a fresh branch off base, pushes it as main, and
// returns its head — a history that diverges from whatever main held.
func (f *fakeGitHub) divergeFrom(name, base string, files map[string]*string, commit func(map[string]*string) string) string {
	work := filepath.Join(f.work, name)
	f.gitCmd(work, "reset", "--quiet", "--hard", base)
	return commit(files)
}

func (f *fakeGitHub) client(token string) *Client {
	return New(token, f.server.URL, WithCacheRoot(f.t.TempDir()))
}

func str(s string) *string { return &s }
