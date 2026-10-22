package business

import (
	"context"
	"fmt"
)

// ModulePrefixOfWorkload names the one composed module a workload credential
// authenticates, from the credential alone: the host's interface reading of a
// workload-owned issue, where no caller types a module name. The credential is
// reviewed by the execution reviewer like every other presented token, and the
// workload it authenticates must be declared by exactly one principal.
func (s *Service) ModulePrefixOfWorkload(ctx context.Context, credential string) (string, error) {
	if s.executionReviewer == nil {
		return "", fmt.Errorf("%w: this host has no execution reviewer wired", ErrExecutionUnbound)
	}
	if credential == "" {
		return "", fmt.Errorf("%w: no credential presented", ErrExecutionNotBoundToken)
	}
	reviewed, err := s.executionReviewer.ReviewToken(ctx, credential, executionTokenAudience)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrExecutionTokenRejected, err)
	}
	modules := s.moduleRegistrySnapshot().registry
	owners := principalsDeclaringWorkload(modules, reviewed.Namespace, reviewed.ServiceAccount)
	if len(owners) != 1 {
		return "", fmt.Errorf("%w: %s/%s is declared by %d principals", ErrExecutionWorkloadAmbiguous,
			reviewed.Namespace, reviewed.ServiceAccount, len(owners))
	}
	return modules[owners[0]].Prefix, nil
}
