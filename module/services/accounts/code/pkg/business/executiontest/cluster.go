// Package executiontest stands in for the independent source a module mint is
// bound by: a Kubernetes API that reviews projected ServiceAccount tokens and
// reports what each pod holds.
//
// It is a fake of that SOURCE, never of the check. BindExecution runs exactly
// as it does on a host — it is handed this Cluster through SetExecutionBinding —
// so a test that mints through it exercises the real comparison: the token's
// audience, its ServiceAccount and namespace against the principal's declared
// workloads, and the live pod's UID and containers against the token's.
//
// What the fake owns is only what Kubernetes would answer. In particular a token is valid for exactly the audience it was issued
// for, as the API server holds it: Review refuses any other with
// business.ErrExecutionTokenRejected, which is what a TokenReview answers.
package executiontest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"accounts/pkg/business"
)

// Audience is the audience accounts reviews a module's token for.
const Audience = "accounts"

// Cluster is the fake API server.
type Cluster struct {
	mu     sync.Mutex
	tokens map[string]issuedToken
	pods   map[podKey]*pod
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
	issuedAt  time.Time
	expiresAt time.Time
}

type podKey struct{ namespace, name string }

type pod struct {
	uid         string
	phase       string
	terminating bool
	containers  []business.ContainerExecution
}

// New returns an empty cluster with no pods.
func New() *Cluster {
	return &Cluster{
		tokens: map[string]issuedToken{},
		pods:   map[podKey]*pod{},
	}
}

// Workload is where a pod runs and as whom.
type Workload struct {
	Namespace      string
	ServiceAccount string
	Container      string
}

// Run schedules one pod for a workload whose container is running, and returns
// the token Kubernetes projects into it for the accounts audience.
func (c *Cluster) Run(workload Workload) string {
	return c.RunWithAudience(workload, Audience)
}

// RunWithAudience is Run with the projected token minted for another audience,
// the way a pod's token for some other service would be.
func (c *Cluster) RunWithAudience(workload Workload, audience string) string {
	return c.run(projectedToken(audience), workload, audience)
}

// RunWithLifetime is Run with a projected token valid for lifetime, the way a
// projection rendered with another expirationSeconds would be.
func (c *Cluster) RunWithLifetime(workload Workload, lifetime time.Duration) string {
	token := c.run(projectedToken(Audience), workload, Audience)
	c.mu.Lock()
	defer c.mu.Unlock()
	issued := c.tokens[token]
	issued.expiresAt = issued.issuedAt.Add(lifetime)
	c.tokens[token] = issued
	return token
}

// RunWithToken is Run for a test that names the token itself, so a fixture
// shared by many tests can hold it as a constant.
func (c *Cluster) RunWithToken(token string, workload Workload) string {
	return c.run(token, workload, Audience)
}

func (c *Cluster) run(token string, workload Workload, audience string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := workload.ServiceAccount + "-" + random(4)
	uid := random(16)
	c.pods[podKey{workload.Namespace, name}] = &pod{
		uid:   uid,
		phase: business.PodPhaseRunning,
		containers: []business.ContainerExecution{{
			Name: workload.Container, Kind: business.ContainerRegular, State: business.ContainerRunning,
		}},
	}
	now := time.Now().UTC().Truncate(time.Second)
	c.tokens[token] = issuedToken{
		audience: audience, namespace: workload.Namespace, account: workload.ServiceAccount,
		pod: name, uid: uid, issuedAt: now, expiresAt: now.Add(business.MaxExecutionTokenLifetime),
	}
	return token
}

// pod returns the pod a token names. The caller holds c.mu.
func (c *Cluster) podOf(token string) *pod {
	issued := c.tokens[token]
	return c.pods[podKey{issued.namespace, issued.pod}]
}

// AddContainer puts another container in the pod a token names: a sidecar
// (regular), an init container, or an ephemeral (debug) container, in state.
func (c *Cluster) AddContainer(token string, container business.ContainerExecution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running := c.podOf(token); running != nil {
		running.containers = append(running.containers, container)
	}
}

// SetContainerState changes the live state of one container of the pod a token
// names, keeping its UID: a stop between review and read.
func (c *Cluster) SetContainerState(token, name string, state business.ContainerState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running := c.podOf(token); running != nil {
		for i := range running.containers {
			if running.containers[i].Name == name {
				running.containers[i].State = state
			}
		}
	}
}

// SetPhase sets the pod's phase, and Delete marks it terminating; both keep
// its UID.
func (c *Cluster) SetPhase(token, phase string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running := c.podOf(token); running != nil {
		running.phase = phase
	}
}

// Delete marks the pod a token names as being deleted.
func (c *Cluster) Delete(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running := c.podOf(token); running != nil {
		running.terminating = true
	}
}

// Replace swaps the pod a token names for a new pod at the same name, the way a
// workload's replacement pod reuses a name with a new UID.
func (c *Cluster) Replace(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running := c.podOf(token); running != nil {
		running.uid = random(16)
	}
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
		IssuedAt:       issued.issuedAt,
		ExpiresAt:      issued.expiresAt,
	}, nil
}

// ReadPod answers as a pod read does.
func (c *Cluster) ReadPod(_ context.Context, namespace, name string) (*business.PodExecution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Unreachable {
		return nil, fmt.Errorf("the api server did not answer")
	}
	running, ok := c.pods[podKey{namespace, name}]
	if !ok {
		return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
	}
	return &business.PodExecution{
		UID: running.uid, Phase: running.phase, Terminating: running.terminating,
		Containers: append([]business.ContainerExecution(nil), running.containers...),
	}, nil
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var _ business.ExecutionReviewer = (*Cluster)(nil)

// NativeDevelopment identifies this in-memory cluster fixture; it models no mesh.
func (*Cluster) NativeDevelopment() bool { return true }

// projectedToken is shaped as the kubelet's projected token is, a compact JWS
// whose claims name its audience, so a workload reads the audience it was
// issued for (a hop's registration proof names it). The fake reviews a token
// by its exact text and verifies no signature.
func projectedToken(audience string) string {
	encode := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"aud": []string{audience}, "jti": random(16)})
	return encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims) + "." + encode([]byte(random(32)))
}
