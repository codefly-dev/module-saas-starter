package business

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// testModuleExecution is the independent source a module mint is bound by, for
// a test in this package: a token names a pod, and the pod holds the declared
// container. BindExecution itself runs unchanged against it.
//
// It lives here rather than in executiontest because that package imports this
// one, and a test inside it cannot import it back.
type testModuleExecution struct {
	mu          sync.Mutex
	tokens      map[string]ReviewedExecutionToken
	audiences   map[string]string
	containers  map[string]string // token -> the declared container its pod runs
	unreachable bool
}

// bindTestModule wires execution binding on s (once) and returns a projected
// token for the first declared workload of prefix.
func bindTestModule(s *Service, prefix string) string {
	if s.moduleAuthority == nil {
		withCurrentAuthority(s)
	}
	execution, ok := s.executionReviewer.(*testModuleExecution)
	if !ok {
		execution = &testModuleExecution{
			tokens: map[string]ReviewedExecutionToken{}, audiences: map[string]string{}, containers: map[string]string{},
		}
		s.SetExecutionBinding(execution)
	}
	grant := s.declaredModules()[ModulePrincipalID(prefix)]
	var workload ModuleWorkload
	if len(grant.Workloads) > 0 {
		workload = grant.Workloads[0]
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	n := len(execution.tokens) + 1
	token := fmt.Sprintf("projected-%s-%d", prefix, n)
	now := time.Now().UTC()
	execution.tokens[token] = ReviewedExecutionToken{
		ServiceAccount: workload.ServiceAccount, Namespace: workload.Namespace,
		PodName: fmt.Sprintf("%s-%d", prefix, n), PodUID: fmt.Sprintf("uid-%s-%d", prefix, n),
		IssuedAt: now, ExpiresAt: now.Add(MaxExecutionTokenLifetime),
	}
	execution.audiences[token] = executionTokenAudience
	execution.containers[token] = workload.Container
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
			}}}, nil
		}
	}
	return nil, fmt.Errorf("pod %s not found", pod)
}

// NativeDevelopment identifies this in-memory cluster fixture; it models no mesh.
func (*testModuleExecution) NativeDevelopment() bool { return true }
