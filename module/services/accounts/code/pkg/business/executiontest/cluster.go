// Package executiontest stands in for the two independent sources a module mint
// is bound by: a Kubernetes API that reviews projected ServiceAccount tokens and
// reports what each pod is running, and an authority that approves one build per
// module principal.
//
// It is a fake of those SOURCES, never of the check. BindExecution runs exactly
// as it does on a host — it is handed this Cluster through SetExecutionBinding —
// so a test that mints through it exercises the real comparison: the token's
// audience, its ServiceAccount and namespace against the principal's declared
// workload, the live pod's UID against the token's, and the RUNNING image against
// the approved build.
//
// What the fake owns is only what Kubernetes and the authority document would
// answer. In particular a token is valid for exactly the audience it was issued
// for, as the API server holds it: Review refuses any other with
// business.ErrExecutionTokenRejected, which is what a TokenReview answers.
package executiontest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"accounts/pkg/business"
)

// Audience is the audience accounts reviews a module's token for.
const Audience = "accounts"

// Cluster is the fake API server and approved-build authority.
type Cluster struct {
	mu        sync.Mutex
	tokens    map[string]issuedToken
	pods      map[podKey]*pod
	approvals map[string]approval
	// Unreachable makes every review and pod read fail as an outage, the
	// way an unreachable API server does.
	Unreachable bool
}

type issuedToken struct {
	audience  string
	namespace string
	account   string
	pod       string
	uid       string
}

type podKey struct{ namespace, name string }

type pod struct {
	uid        string
	containers map[string]string // container -> running imageID
	declared   map[string]string // container -> spec image
}

type approval struct {
	digest      business.ApprovedDigest
	incarnation uint64
}

// New returns an empty cluster with no pods and no approvals.
func New() *Cluster {
	return &Cluster{
		tokens:    map[string]issuedToken{},
		pods:      map[podKey]*pod{},
		approvals: map[string]approval{},
	}
}

// Workload is where a pod runs and as whom.
type Workload struct {
	Namespace      string
	ServiceAccount string
	Container      string
}

// Run schedules one pod for a workload whose container is RUNNING image, and
// returns the token Kubernetes projects into it for the accounts audience.
func (c *Cluster) Run(workload Workload, image string) string {
	return c.RunWithAudience(workload, image, Audience)
}

// RunWithAudience is Run with the projected token minted for another audience,
// the way a pod's token for some other service would be.
func (c *Cluster) RunWithAudience(workload Workload, image, audience string) string {
	return c.run("projected."+random(16), workload, image, audience)
}

// RunWithToken is Run for a test that names the token itself, so a fixture
// shared by many tests can hold it as a constant.
func (c *Cluster) RunWithToken(token string, workload Workload, image string) string {
	return c.run(token, workload, image, Audience)
}

func (c *Cluster) run(token string, workload Workload, image, audience string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := workload.ServiceAccount + "-" + random(4)
	uid := random(16)
	c.pods[podKey{workload.Namespace, name}] = &pod{
		uid:        uid,
		containers: map[string]string{workload.Container: image},
		declared:   map[string]string{workload.Container: image},
	}
	c.tokens[token] = issuedToken{
		audience: audience, namespace: workload.Namespace, account: workload.ServiceAccount,
		pod: name, uid: uid,
	}
	return token
}

// Replace swaps the pod a token names for a new pod at the same name, the way a
// workload's replacement pod reuses a name with a new UID.
func (c *Cluster) Replace(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	issued := c.tokens[token]
	if running, ok := c.pods[podKey{issued.namespace, issued.pod}]; ok {
		running.uid = random(16)
	}
}

// Retag makes the pod a token names RUN image while its spec still ASKS for
// what it asked for: a moved tag.
func (c *Cluster) Retag(token, image string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	issued := c.tokens[token]
	if running, ok := c.pods[podKey{issued.namespace, issued.pod}]; ok {
		for name := range running.containers {
			running.containers[name] = image
		}
	}
}

// Approve records the build the authority document approves for a principal,
// named as the document names it: by module prefix.
func (c *Cluster) Approve(prefix, digest string, incarnation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approvals[prefix] = approval{digest: business.ApprovedDigest(digest), incarnation: incarnation}
}

// ReviewToken answers as a TokenReview does.
func (c *Cluster) ReviewToken(_ context.Context, token, audience string) (*business.ReviewedExecutionToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Unreachable {
		return nil, fmt.Errorf("the api server did not answer")
	}
	issued, ok := c.tokens[token]
	if !ok {
		return nil, fmt.Errorf("%w: unknown token", business.ErrExecutionTokenRejected)
	}
	if issued.audience != audience {
		return nil, fmt.Errorf("%w: token audiences [%s] do not include %s",
			business.ErrExecutionTokenRejected, issued.audience, audience)
	}
	return &business.ReviewedExecutionToken{
		ServiceAccount: issued.account,
		Namespace:      issued.namespace,
		PodName:        issued.pod,
		PodUID:         issued.uid,
	}, nil
}

// RunningContainer answers as a pod read does.
func (c *Cluster) RunningContainer(
	_ context.Context, namespace, name, container string,
) (*business.RunningContainerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Unreachable {
		return nil, fmt.Errorf("the api server did not answer")
	}
	running, ok := c.pods[podKey{namespace, name}]
	if !ok {
		return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
	}
	image, found := running.containers[container]
	return &business.RunningContainerStatus{
		UID: running.uid, ImageID: image, DeclaredImage: running.declared[container], Found: found,
	}, nil
}

// ApprovedBuild answers as the approved-build view does.
func (c *Cluster) ApprovedBuild(_ context.Context, principal string) (business.ApprovedDigest, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	approved, ok := c.approvals[principal]
	if !ok {
		return "", 0, fmt.Errorf("%w: %s", business.ErrUnknownExecutionPrincipal, principal)
	}
	return approved.digest, approved.incarnation, nil
}

// Image is a digest-pinned reference for a fake build, and Digest its digest.
func Image(build string) string { return "registry.example/" + build + "@" + Digest(build) }

// Digest is a stable sha256-shaped digest for a named fake build.
func Digest(build string) string {
	sum := make([]byte, 32)
	copy(sum, build)
	return "sha256:" + hex.EncodeToString(sum)
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var (
	_ business.ExecutionReviewer  = (*Cluster)(nil)
	_ business.ExecutionAuthority = (*Cluster)(nil)
)
