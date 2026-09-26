package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Repository content travels over git's smart-HTTP transport, not the REST API.
// A REST read costs one call per file against a budget GitHub meters per
// credential (and, for a public repository read with no credential, per source
// IP at 60 an hour), so a snapshot of more files than the remaining budget could
// never complete. Git transport is not metered against that budget, and it
// fetches a whole change set in one request: a snapshot is one fetch of the
// pinned commit's trees and one batched fetch of the in-scope blobs, whatever
// their number.
//
// Each source keeps a bare, partial mirror of its repository on the host's own
// disk, under the source's org and id, so nothing is ever shared across tenants
// or sources. The mirror is a cache, never a record: losing it (a restart, an
// eviction) costs one refetch, and a mirror past its size bound is discarded and
// the operation refused rather than allowed to grow.

// Limits a mirror enforces. Each fails closed: the operation is refused with a
// typed error rather than partly served.
const (
	// MaxFileBytes is the largest single file a mirror serves.
	MaxFileBytes = 25 << 20
	// gitTimeout bounds every git invocation.
	gitTimeout = 5 * time.Minute
	// gitStderrCap bounds the diagnostics read back from git.
	gitStderrCap = 16 << 10
)

// Bounds a mirror enforces; variables only so tests can lower them.
var (
	// maxMirrorBytes bounds one source's mirror on disk. A fetch that would grow
	// it past this is killed, the mirror discarded, and the operation refused.
	maxMirrorBytes int64 = 512 << 20
	// maxListedFiles bounds the in-scope files one version may list.
	maxListedFiles = 10000
	// maxChangedFiles bounds the files one comparison may report; a larger diff
	// is reported Truncated and reconciled with a snapshot instead.
	maxChangedFiles = 1000
	// maxCacheBytes bounds every mirror under one cache root together. When a
	// sweep finds them past it, the least recently used mirrors no operation
	// holds are discarded until they fit.
	maxCacheBytes int64 = 2 << 30
	// sweepInterval spaces the sweeps a process runs.
	sweepInterval = 10 * time.Minute
)

// Typed refusals a mirror reports.
var (
	// ErrGitUnavailable reports a host with no git binary on its PATH.
	ErrGitUnavailable = errors.New("github: git is not installed on this host")
	// ErrRepositoryTooLarge reports a mirror that would grow past its bound.
	ErrRepositoryTooLarge = errors.New("github: repository exceeds the host's mirror size limit")
	// ErrTooManyFiles reports a version listing more in-scope files than a
	// source may hold.
	ErrTooManyFiles = errors.New("github: version lists more in-scope files than a source may hold")
)

// Workspace names the per-source mirror: the org that owns the source, and the
// source. Both are host-issued UUIDs; anything else is refused before a path is
// built from it.
type Workspace struct {
	Org    string
	Source string
}

var workspaceIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidObjectID reports whether id is a full, lowercase git object id.
func ValidObjectID(id string) bool { return objectIDPattern.MatchString(id) }

func (w Workspace) valid() bool {
	return workspaceIDPattern.MatchString(w.Org) && workspaceIDPattern.MatchString(w.Source) &&
		!strings.Contains(w.Org, "..") && !strings.Contains(w.Source, "..")
}

// mirrorLocks serializes a process's operations on one mirror; the file lock
// taken beside it serializes processes that share the cache root.
var mirrorLocks sync.Map

// OpenRepository opens (creating on first use) the source's mirror of repo and
// holds it exclusively until Close. Every operation on a Repository reads the
// mirror; only Fetch-style operations reach the network, and each does so in
// one request.
func (c *Client) OpenRepository(ctx context.Context, ws Workspace, repo string) (*Repository, error) {
	if !ws.valid() {
		return nil, fmt.Errorf("github: invalid mirror workspace")
	}
	if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return nil, fmt.Errorf("github: invalid repository %q", repo)
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitUnavailable
	}
	dir := filepath.Join(c.cacheRoot, "mirrors", ws.Org, ws.Source)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("github: create mirror directory: %w", err)
	}
	muAny, _ := mirrorLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("github: open mirror lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		mu.Unlock()
		return nil, fmt.Errorf("github: lock mirror: %w", err)
	}
	// Mark the mirror used, for the sweep's least-recently-used order.
	now := time.Now()
	_ = os.Chtimes(filepath.Join(dir, "lock"), now, now)
	r := &Repository{
		client: c,
		bin:    bin,
		dir:    filepath.Join(dir, "repo.git"),
		remote: c.gitBaseURL + "/" + repo + ".git",
		release: func() {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			_ = lock.Close()
			mu.Unlock()
		},
	}
	if err := r.prepare(ctx); err != nil {
		r.release()
		return nil, err
	}
	c.maybeSweep(dir)
	return r, nil
}

var lastSweep sync.Map // cache root → time.Time

// maybeSweep runs sweep at most once per sweepInterval per cache root.
func (c *Client) maybeSweep(holding string) {
	now := time.Now()
	if last, ok := lastSweep.Load(c.cacheRoot); ok && now.Sub(last.(time.Time)) < sweepInterval {
		return
	}
	lastSweep.Store(c.cacheRoot, now)
	sweepMirrors(filepath.Join(c.cacheRoot, "mirrors"), maxCacheBytes, holding)
}

// sweepMirrors discards the least recently used mirrors under root until the
// rest fit in limit. A mirror another operation holds is skipped, never waited
// on, and so is holding (the caller's own).
func sweepMirrors(root string, limit int64, holding string) {
	type mirror struct {
		dir  string
		used time.Time
		size int64
	}
	var mirrors []mirror
	var total int64
	orgs, _ := os.ReadDir(root)
	for _, org := range orgs {
		sources, _ := os.ReadDir(filepath.Join(root, org.Name()))
		for _, src := range sources {
			dir := filepath.Join(root, org.Name(), src.Name())
			info, err := os.Stat(filepath.Join(dir, "lock"))
			if err != nil {
				continue
			}
			size, _ := dirSize(dir)
			total += size
			mirrors = append(mirrors, mirror{dir: dir, used: info.ModTime(), size: size})
		}
	}
	sort.Slice(mirrors, func(i, j int) bool { return mirrors[i].used.Before(mirrors[j].used) })
	for _, m := range mirrors {
		if total <= limit {
			return
		}
		if m.dir == holding {
			continue
		}
		lock, err := os.OpenFile(filepath.Join(m.dir, "lock"), os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			_ = lock.Close()
			continue
		}
		if os.RemoveAll(filepath.Join(m.dir, "repo.git")) == nil {
			total -= m.size
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
}

// Repository is one source's mirror, held exclusively between OpenRepository
// and Close.
type Repository struct {
	client  *Client
	bin     string
	dir     string
	remote  string
	release func()
	once    sync.Once
	// fetches counts the network fetches this handle made, for observability.
	fetches int
}

// Close releases the mirror.
func (r *Repository) Close() error {
	r.once.Do(r.release)
	return nil
}

// Fetches reports how many network fetches this handle has made.
func (r *Repository) Fetches() int { return r.fetches }

// prepare makes r.dir a bare partial mirror of r.remote, discarding one that is
// unreadable, names another remote, or has outgrown its bound.
func (r *Repository) prepare(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(r.dir, "HEAD")); err == nil {
		var out bytes.Buffer
		err := r.run(ctx, false, nil, &out, "config", "--get", "remote.origin.url")
		size, sizeErr := dirSize(r.dir)
		if err == nil && strings.TrimSpace(out.String()) == r.remote && sizeErr == nil && size <= maxMirrorBytes {
			return nil
		}
	}
	if err := os.RemoveAll(r.dir); err != nil {
		return fmt.Errorf("github: discard mirror: %w", err)
	}
	if err := r.run(ctx, false, nil, nil, "init", "--quiet", "--bare", r.dir); err != nil {
		return err
	}
	settings := [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", r.remote},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
		{"fetch.writeCommitGraph", "false"},
	}
	for _, kv := range settings {
		if err := r.run(ctx, false, nil, nil, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// discard removes the mirror after a refusal that leaves it unusable.
func (r *Repository) discard() { _ = os.RemoveAll(r.dir) }

// List returns every regular file at commit whose path match accepts, with its
// blob id. It fetches the commit and its trees — never a blob — in at most two
// requests, and refuses a version listing more than maxListedFiles matches.
// Size is -1: blob sizes are known only once Fetch has made the blobs local.
func (r *Repository) List(ctx context.Context, commit string, match func(path string) bool) ([]File, error) {
	if err := r.ensureTrees(ctx, commit); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := r.run(ctx, false, nil, &out, "ls-tree", "-r", "-z", "--full-tree", commit); err != nil {
		return nil, err
	}
	var files []File
	for _, entry := range bytes.Split(out.Bytes(), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("github: unreadable tree entry")
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		if !match(string(path)) {
			continue
		}
		if len(files) == maxListedFiles {
			return nil, ErrTooManyFiles
		}
		files = append(files, File{Path: string(path), SHA: fields[2], Size: -1})
	}
	return files, nil
}

// Compare reports how head relates to base and, when head is ahead of base,
// the files that changed between them. Renames are exact only: a file both moved
// and edited is reported as a removal and an addition, which no consumer can
// tell apart from a rename at the level of the content it ingests. A base the
// remote can no longer serve (a force push that orphaned it) is ErrNotFound.
func (r *Repository) Compare(ctx context.Context, base, head string) (*Comparison, error) {
	if !ValidObjectID(base) || !ValidObjectID(head) {
		return nil, fmt.Errorf("github: compare needs two commit ids")
	}
	if base == head {
		return &Comparison{Status: CompareStatusIdentical}, nil
	}
	if err := r.ensureCommits(ctx, base, head); err != nil {
		return nil, err
	}
	ahead, err := r.isAncestor(ctx, base, head)
	if err != nil {
		return nil, err
	}
	if !ahead {
		behind, err := r.isAncestor(ctx, head, base)
		if err != nil {
			return nil, err
		}
		if behind {
			return &Comparison{Status: CompareStatusBehind}, nil
		}
		return &Comparison{Status: CompareStatusDiverged}, nil
	}
	if err := r.ensureTrees(ctx, base, head); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := r.run(ctx, false, nil, &out, "diff-tree", "-r", "-z", "--raw", "-M100%", base, head); err != nil {
		return nil, err
	}
	files, err := parseRawDiff(out.Bytes())
	if err != nil {
		return nil, err
	}
	result := &Comparison{Status: CompareStatusAhead, Files: files}
	if len(files) > maxChangedFiles {
		result.Files, result.Truncated = files[:maxChangedFiles], true
	}
	return result, nil
}

// parseRawDiff reads `git diff-tree -r -z --raw` output: for each change a
// ":srcmode dstmode srcid dstid status" record, then its path (and, for a rename
// or copy, the destination path).
func parseRawDiff(raw []byte) ([]ChangedFile, error) {
	parts := bytes.Split(raw, []byte{0})
	var files []ChangedFile
	for i := 0; i < len(parts); i++ {
		meta := string(parts[i])
		if meta == "" {
			continue
		}
		if !strings.HasPrefix(meta, ":") {
			return nil, fmt.Errorf("github: unreadable diff record")
		}
		fields := strings.Fields(meta[1:])
		if len(fields) != 5 || i+1 >= len(parts) {
			return nil, fmt.Errorf("github: unreadable diff record")
		}
		srcMode, dstMode, dstID, status := fields[0], fields[1], fields[3], fields[4]
		path := string(parts[i+1])
		i++
		regular := func(mode string) bool { return mode == "100644" || mode == "100755" }
		switch status[0] {
		case 'A':
			if regular(dstMode) {
				files = append(files, ChangedFile{Filename: path, Status: "added", SHA: dstID})
			}
		case 'D':
			if regular(srcMode) {
				files = append(files, ChangedFile{Filename: path, Status: "removed"})
			}
		case 'M', 'T':
			switch {
			case regular(srcMode) && regular(dstMode):
				files = append(files, ChangedFile{Filename: path, Status: "modified", SHA: dstID})
			case regular(dstMode):
				files = append(files, ChangedFile{Filename: path, Status: "added", SHA: dstID})
			case regular(srcMode):
				files = append(files, ChangedFile{Filename: path, Status: "removed"})
			}
		case 'R', 'C':
			if i+1 >= len(parts) {
				return nil, fmt.Errorf("github: unreadable diff record")
			}
			to := string(parts[i+1])
			i++
			kind := "renamed"
			if status[0] == 'C' {
				kind = "copied"
			}
			if regular(dstMode) {
				files = append(files, ChangedFile{Filename: to, PreviousFilename: path, Status: kind, SHA: dstID})
			}
		}
	}
	return files, nil
}

// Fetch makes the named blobs local, in one request for all of those the mirror
// does not already hold. It is the whole network cost of reading a change set.
func (r *Repository) Fetch(ctx context.Context, ids []string) error {
	missing, err := r.missing(ctx, ids)
	if err != nil || len(missing) == 0 {
		return err
	}
	return r.fetchObjects(ctx, "blob:none", missing)
}

// Sizes reports each named blob's byte length. Every blob must already be
// local (see Fetch); a missing one is an error, never a lazy network read.
func (r *Repository) Sizes(ctx context.Context, ids []string) (map[string]int64, error) {
	out, err := r.batchCheck(ctx, ids)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(ids))
	for id, entry := range out {
		if entry.kind != "blob" {
			return nil, fmt.Errorf("github: object %s is not a local blob", id)
		}
		sizes[id] = entry.size
	}
	return sizes, nil
}

// Read returns one local blob's bytes, refusing one past max with
// ErrFileTooLarge before reading it.
func (r *Repository) Read(ctx context.Context, id string, max int64) ([]byte, error) {
	sizes, err := r.Sizes(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if sizes[id] > max {
		return nil, ErrFileTooLarge
	}
	var out bytes.Buffer
	if err := r.run(ctx, false, nil, &out, "cat-file", "blob", id); err != nil {
		return nil, err
	}
	if int64(out.Len()) != sizes[id] {
		return nil, fmt.Errorf("github: blob %s read short", id)
	}
	return out.Bytes(), nil
}

// Stream hands visit each named local blob, in order, from a single reader
// process. visit must consume exactly size bytes from content or return an
// error; the stream stops at the first error.
func (r *Repository) Stream(ctx context.Context, ids []string, visit func(id string, size int64, content io.Reader) error) error {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := r.command(ctx, false, "--git-dir", r.dir, "cat-file", "--batch")
	var stdin bytes.Buffer
	for _, id := range ids {
		if !ValidObjectID(id) {
			return fmt.Errorf("github: invalid object id")
		}
		stdin.WriteString(id + "\n")
	}
	cmd.Stdin = &stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{max: gitStderrCap}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	streamErr := func() error {
		for _, id := range ids {
			header, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("github: read blob header: %w", err)
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
				return fmt.Errorf("github: object %s is not a local blob", id)
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return fmt.Errorf("github: unreadable blob size")
			}
			content := &io.LimitedReader{R: reader, N: size}
			if err := visit(id, size, content); err != nil {
				return err
			}
			if content.N != 0 {
				return fmt.Errorf("github: blob %s was not read whole", id)
			}
			if b, err := reader.ReadByte(); err != nil || b != '\n' {
				return fmt.Errorf("github: blob %s ended unexpectedly", id)
			}
		}
		return nil
	}()
	if streamErr != nil {
		cancel()
		_ = cmd.Wait()
		return streamErr
	}
	if err := cmd.Wait(); err != nil {
		return r.classify(ctx, err, stderr.String())
	}
	return nil
}

type objectInfo struct {
	kind string
	size int64
}

// batchCheck reads the local type and size of each object, reporting a missing
// one as kind "missing". Lazy fetching is disabled, so it never touches the
// network.
func (r *Repository) batchCheck(ctx context.Context, ids []string) (map[string]objectInfo, error) {
	info := make(map[string]objectInfo, len(ids))
	if len(ids) == 0 {
		return info, nil
	}
	var stdin bytes.Buffer
	for _, id := range ids {
		if !ValidObjectID(id) {
			// No object can be named by a malformed id.
			return nil, fmt.Errorf("%w: malformed object id", ErrNotFound)
		}
		stdin.WriteString(id + "\n")
	}
	var out bytes.Buffer
	if err := r.run(ctx, false, &stdin, &out, "cat-file", "--batch-check"); err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[1] == "missing":
			info[fields[0]] = objectInfo{kind: "missing"}
		case len(fields) == 3:
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("github: unreadable object size")
			}
			info[fields[0]] = objectInfo{kind: fields[1], size: size}
		default:
			return nil, fmt.Errorf("github: unreadable object record")
		}
	}
	return info, nil
}

func (r *Repository) missing(ctx context.Context, ids []string) ([]string, error) {
	info, err := r.batchCheck(ctx, ids)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var missing []string
	for _, id := range ids {
		if info[id].kind == "missing" && !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	return missing, nil
}

// ensureCommits fetches the commit graph (commits only, no trees or blobs)
// reaching each commit the mirror does not hold, in one request.
func (r *Repository) ensureCommits(ctx context.Context, commits ...string) error {
	for _, commit := range commits {
		if !ValidObjectID(commit) {
			return fmt.Errorf("github: invalid commit id")
		}
	}
	missing, err := r.missing(ctx, commits)
	if err != nil || len(missing) == 0 {
		return err
	}
	return r.fetchRefs(ctx, "tree:0", missing)
}

// ensureTrees makes each commit's whole tree (no blobs) local, in one request
// for those the mirror does not hold.
func (r *Repository) ensureTrees(ctx context.Context, commits ...string) error {
	if err := r.ensureCommits(ctx, commits...); err != nil {
		return err
	}
	trees := make([]string, 0, len(commits))
	for _, commit := range commits {
		var out bytes.Buffer
		if err := r.run(ctx, false, nil, &out, "cat-file", "commit", commit); err != nil {
			return err
		}
		first, _, _ := strings.Cut(out.String(), "\n")
		tree, ok := strings.CutPrefix(first, "tree ")
		if !ok || !ValidObjectID(tree) {
			return fmt.Errorf("github: unreadable commit %s", commit)
		}
		trees = append(trees, tree)
	}
	missing, err := r.missing(ctx, trees)
	if err != nil || len(missing) == 0 {
		return err
	}
	return r.fetchObjects(ctx, "blob:none", missing)
}

func (r *Repository) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	err := r.run(ctx, false, nil, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exit *gitExitError
	if errors.As(err, &exit) && exit.code == 1 {
		return false, nil
	}
	return false, err
}

// fetchRefs fetches commits by id under filter, negotiating with what the
// mirror holds so only new history crosses the wire.
func (r *Repository) fetchRefs(ctx context.Context, filter string, ids []string) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--filter=" + filter, "origin"}
	return r.fetch(ctx, append(args, ids...), nil)
}

// fetchObjects fetches objects by id — the way git itself backfills a partial
// clone — with no negotiation, so exactly the named objects (and, for a tree,
// its subtrees) are sent.
func (r *Repository) fetchObjects(ctx context.Context, filter string, ids []string) error {
	var stdin bytes.Buffer
	for _, id := range ids {
		stdin.WriteString(id + "\n")
	}
	args := []string{"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head",
		"--recurse-submodules=no", "--filter=" + filter, "--stdin", "origin"}
	return r.fetch(ctx, args, &stdin)
}

// fetch runs one network fetch under the mirror's size bound: a watchdog kills
// it when the mirror outgrows maxMirrorBytes, and the mirror is discarded.
func (r *Repository) fetch(ctx context.Context, args []string, stdin io.Reader) error {
	r.fetches++
	r.client.recordFetch()
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var exceeded bool
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if size, err := dirSize(r.dir); err == nil && size > maxMirrorBytes {
					mu.Lock()
					exceeded = true
					mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	err := r.run(fetchCtx, true, stdin, nil, args...)
	close(done)
	mu.Lock()
	over := exceeded
	mu.Unlock()
	if !over {
		if size, sizeErr := dirSize(r.dir); sizeErr == nil && size > maxMirrorBytes {
			over = true
		}
	}
	if over {
		r.discard()
		return ErrRepositoryTooLarge
	}
	return err
}

// run executes git against the mirror. network marks the invocations allowed to
// reach the remote; every other one runs with lazy fetching disabled, so a
// missing object is an error rather than a hidden per-object request.
func (r *Repository) run(ctx context.Context, network bool, stdin io.Reader, stdout io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := args
	if len(args) == 0 || args[0] != "init" {
		full = append([]string{"--git-dir", r.dir}, args...)
	}
	cmd := r.command(ctx, network, full...)
	cmd.Stdin = stdin
	if stdout != nil {
		cmd.Stdout = stdout
	}
	stderr := &cappedBuffer{max: gitStderrCap}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return r.classify(ctx, err, stderr.String())
	}
	return nil
}

// command builds a git invocation with a closed environment: no user or system
// configuration, no credential helpers, no prompts, HTTPS (or, for a test
// remote, HTTP) as the only transport, and the source's credential — if it has
// one — as an Authorization header scoped to the remote's origin. The credential
// travels in the environment, never on the command line or in the URL, and is
// never logged.
func (r *Repository) command(ctx context.Context, network bool, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.WaitDelay = 5 * time.Second
	// Run from the cache root so git never discovers an ambient repository.
	cmd.Dir = r.client.cacheRoot
	home := filepath.Join(r.client.cacheRoot, "home")
	_ = os.MkdirAll(home, 0o700)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"LC_ALL=C",
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	if !network {
		env = append(env, "GIT_NO_LAZY_FETCH=1")
	}
	config := [][2]string{
		{"credential.helper", ""},
		{"protocol.allow", "never"},
		{"protocol." + r.client.gitScheme + ".allow", "always"},
		{"core.hooksPath", os.DevNull},
	}
	if network && r.client.token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.client.token))
		config = append(config, [2]string{"http." + r.client.gitBaseURL + "/.extraHeader", "Authorization: Basic " + basic})
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	cmd.Env = env
	return cmd
}

// gitExitError is a git invocation that exited non-zero for a reason classify
// has no sentinel for. Its message carries git's own diagnostics with any
// credential removed.
type gitExitError struct {
	code   int
	detail string
}

func (e *gitExitError) Error() string {
	return fmt.Sprintf("github: git exited %d: %s", e.code, e.detail)
}

// classify maps a failed git invocation onto the package's sentinels.
func (r *Repository) classify(ctx context.Context, err error, stderr string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("github: git did not finish: %w", ctxErr)
	}
	return classifyGitFailure(err, stderr, r.client.token)
}

func classifyGitFailure(err error, stderr, token string) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "returned error: 429") || strings.Contains(lower, "rate limit"):
		return &RateLimitError{Unauthenticated: token == ""}
	case strings.Contains(lower, "could not read username") || strings.Contains(lower, "returned error: 401") ||
		strings.Contains(lower, "authentication failed"):
		if token == "" {
			// GitHub answers an unauthenticated read of a private repository the
			// same as a missing one.
			return ErrNotFound
		}
		return ErrUnauthorized
	case strings.Contains(lower, "returned error: 403"):
		return ErrForbidden
	case strings.Contains(lower, "repository not found") || strings.Contains(lower, "returned error: 404") ||
		(strings.Contains(lower, "repository ") && strings.Contains(lower, " not found")) ||
		strings.Contains(lower, "not our ref") || strings.Contains(lower, "couldn't find remote ref") ||
		strings.Contains(lower, "no such remote ref") || strings.Contains(lower, "unadvertised object") ||
		strings.Contains(lower, "did not send all necessary objects"):
		return ErrNotFound
	}
	code := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	detail := strings.TrimSpace(stderr)
	if token != "" {
		detail = strings.ReplaceAll(detail, token, "[redacted]")
		detail = strings.ReplaceAll(detail, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "[redacted]")
	}
	if len(detail) > 512 {
		detail = detail[:512]
	}
	return &gitExitError{code: code, detail: detail}
}

// lsRemote lists the remote's refs matching patterns, in one request.
func (c *Client) lsRemote(ctx context.Context, repo string, flags []string, patterns ...string) (string, error) {
	if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return "", fmt.Errorf("github: invalid repository %q", repo)
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", ErrGitUnavailable
	}
	r := &Repository{client: c, bin: bin, remote: c.gitBaseURL + "/" + repo + ".git"}
	c.recordFetch()
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if err := os.MkdirAll(c.cacheRoot, 0o700); err != nil {
		return "", err
	}
	args := append(append(append([]string{"ls-remote"}, flags...), "--", r.remote), patterns...)
	cmd := r.command(ctx, true, args...)
	var out bytes.Buffer
	stderr := &cappedBuffer{max: gitStderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return "", r.classify(ctx, err, stderr.String())
	}
	return out.String(), nil
}

// cappedBuffer keeps the first max bytes written to it.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// gitBaseURLFor derives the git transport origin from the REST API base URL:
// api.github.com serves REST for github.com, and a GitHub Enterprise Server
// serves REST under /api/v3 of the origin that serves git. Any other base (a
// test server) serves both.
func gitBaseURLFor(apiBase string) (string, string) {
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		return "https://github.com", "https"
	}
	if strings.EqualFold(u.Host, "api.github.com") {
		return "https://github.com", "https"
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v3")
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), u.Scheme
}
