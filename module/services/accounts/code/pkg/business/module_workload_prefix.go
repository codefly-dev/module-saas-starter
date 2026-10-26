package business

import (
	"context"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/wool"
)

// ModulePrefixOfWorkload names the one composed module a workload credential
// authenticates, from the credential alone: the host's interface reading of a
// workload-owned issue, where no caller types a module name. The credential is
// reviewed by the execution reviewer like every other presented token, and the
// workload it authenticates must be declared by exactly one principal.
func (s *Service) ModulePrefixOfWorkload(ctx context.Context, credential string) (prefix string, err error) {
	defer func() {
		if err != nil {
			reason := err.Error()
			if credential != "" {
				reason = strings.ReplaceAll(reason, credential, "[redacted workload credential]")
			}
			wool.Get(ctx).In("ModulePrefixOfWorkload").Warn("workload credential refused", wool.Field("reason", reason))
		}
	}()
	reviewed, err := s.reviewWorkloadCredential(ctx, credential)
	if err != nil {
		return "", err
	}
	modules := s.moduleRegistrySnapshot().registry
	owners := principalsDeclaringWorkload(modules, reviewed.Namespace, reviewed.ServiceAccount)
	if len(owners) != 1 {
		return "", fmt.Errorf("%w: %s/%s is declared by %d principals", ErrExecutionWorkloadAmbiguous,
			reviewed.Namespace, reviewed.ServiceAccount, len(owners))
	}
	return modules[owners[0]].Prefix, nil
}

// reviewWorkloadCredential is the workload a credential authenticates, as the
// execution reviewer answers it: every interface method a workload proves
// itself to by credential alone reads its namespace and service account here.
func (s *Service) reviewWorkloadCredential(ctx context.Context, credential string) (*ReviewedExecutionToken, error) {
	if s.executionReviewer == nil {
		return nil, fmt.Errorf("%w: this host has no execution reviewer wired", ErrExecutionUnbound)
	}
	if credential == "" {
		return nil, fmt.Errorf("%w: no credential presented", ErrExecutionNotBoundToken)
	}
	reviewed, err := s.executionReviewer.ReviewToken(ctx, credential, executionTokenAudience)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecutionTokenRejected, err)
	}
	if reviewed == nil {
		return nil, fmt.Errorf("%w: execution reviewer returned no workload identity", ErrExecutionUnbound)
	}
	return reviewed, nil
}
