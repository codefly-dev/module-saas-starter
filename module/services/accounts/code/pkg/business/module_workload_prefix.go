package business

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/wool"
)

// minRedactedCredential is the shortest presented credential redacted from a
// log line. Replacing a shorter one would let a caller garble its own log line
// (a credential "e" would rewrite every "e"), and a credential that short
// proves nothing a log line could leak.
const minRedactedCredential = 16

// RedactWorkloadCredential is text with the presented workload credential
// replaced, for a log line.
func RedactWorkloadCredential(text, credential string) string {
	if len(credential) < minRedactedCredential {
		return text
	}
	return strings.ReplaceAll(text, credential, "[redacted workload credential]")
}

// ModulePrefixOfWorkload names the one composed module a workload credential
// authenticates, from the credential alone: the host's interface reading of a
// workload-owned issue, where no caller types a module name. The credential is
// reviewed by the execution reviewer like every other presented token, and the
// workload it authenticates must be declared by exactly one principal.
func (s *Service) ModulePrefixOfWorkload(ctx context.Context, credential string) (prefix string, err error) {
	defer func() {
		if err != nil {
			reason := RedactWorkloadCredential(err.Error(), credential)
			// A caller ending its own request carries no operational signal.
			// It is the caller's context that says so: a dependency answering
			// Canceled while the caller is live is a failure, logged as one.
			if errors.Is(ctx.Err(), context.Canceled) {
				wool.Get(ctx).In("ModulePrefixOfWorkload").Info("workload credential refused", wool.Field("reason", reason))
				return
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
		// The review RAN and refused the token: a verdict on the caller, as
		// the reviewer marked it. Anything else is a review that could not
		// run (the request ended, the API server did not answer), which
		// decided nothing about the caller, the rule BindExecution applies.
		if errors.Is(err, ErrExecutionTokenRejected) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrExecutionUnbound, err)
	}
	if reviewed == nil {
		return nil, fmt.Errorf("%w: execution reviewer returned no workload identity", ErrExecutionUnbound)
	}
	return reviewed, nil
}
