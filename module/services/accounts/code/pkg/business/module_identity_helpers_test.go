package business

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// testModuleExecution is the two independent sources a module mint is bound by,
// for a test in this package: a token names a pod, a pod runs an image, and a
// principal (named by prefix, as an authority document names it) is approved for
// a build. BindExecution itself runs unchanged against it.
//
// It lives here rather than in executiontest because that package imports this
// one, and a test inside it cannot import it back.
type testModuleExecution struct {
	mu          sync.Mutex
	tokens      map[string]ReviewedExecutionToken
	audiences   map[string]string
	containers  map[string]string // token -> the declared container its pod runs
	running     map[string]string // pod uid -> running image id
	approved    map[string]ApprovedDigest
	unreachable bool
}

const testModuleImage = "registry.example/module@sha256:" +
	"abababababababababababababababababababababababababababababababab"

// bindTestModule wires execution binding on s (once) and returns a projected
// token for the declared workload of prefix, its pod running the approved build.
func bindTestModule(s *Service, prefix string) string {
	if s.moduleAuthority == nil {
		withCurrentAuthority(s)
	}
	execution, ok := s.executionReviewer.(*testModuleExecution)
	if !ok {
		execution = &testModuleExecution{
			tokens: map[string]ReviewedExecutionToken{}, audiences: map[string]string{}, containers: map[string]string{},
			running: map[string]string{}, approved: map[string]ApprovedDigest{},
		}
		s.SetExecutionBinding(execution, execution)
	}
	grant := s.declaredModules()[ModulePrincipalID(prefix)]
	execution.mu.Lock()
	defer execution.mu.Unlock()
	n := len(execution.tokens) + 1
	token := fmt.Sprintf("projected-%s-%d", prefix, n)
	uid := fmt.Sprintf("uid-%s-%d", prefix, n)
	now := time.Now().UTC()
	execution.tokens[token] = ReviewedExecutionToken{
		ServiceAccount: grant.Workload.ServiceAccount, Namespace: grant.Workload.Namespace,
		PodName: fmt.Sprintf("%s-%d", prefix, n), PodUID: uid,
		IssuedAt: now, ExpiresAt: now.Add(MaxExecutionTokenLifetime),
	}
	execution.audiences[token] = executionTokenAudience
	execution.containers[token] = grant.Workload.Container
	execution.running[uid] = testModuleImage
	execution.approved[prefix] = ApprovedDigest(testModuleImage)
	return token
}

func (e *testModuleExecution) ReviewToken(_ context.Context, token, audience string) (*ReviewedExecutionToken, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.unreachable {
		return nil, fmt.Errorf("the api server did not answer")
	}
	reviewed, ok := e.tokens[token]
	if !ok || e.audiences[token] != audience {
		return nil, fmt.Errorf("%w: not authenticated", ErrExecutionTokenRejected)
	}
	return &reviewed, nil
}

func (e *testModuleExecution) ReadPod(_ context.Context, _, pod string) (*PodExecution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for token, reviewed := range e.tokens {
		if reviewed.PodName == pod {
			return &PodExecution{UID: reviewed.PodUID, Phase: PodPhaseRunning, Containers: []ContainerExecution{{
				Name: e.containers[token], Kind: ContainerRegular, State: ContainerRunning,
				ImageID: e.running[reviewed.PodUID],
			}}}, nil
		}
	}
	return nil, fmt.Errorf("pod %s not found", pod)
}

func (e *testModuleExecution) ApprovedBuild(_ context.Context, principal string) (ApprovedDigest, uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	approved, ok := e.approved[principal]
	if !ok {
		return "", 0, ErrUnknownExecutionPrincipal
	}
	return approved, 1, nil
}

// NativeDevelopment identifies this in-memory cluster fixture; it models no mesh.
func (*testModuleExecution) NativeDevelopment() bool { return true }
