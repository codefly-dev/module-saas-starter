package frontendpayload

import (
	"context"
	"errors"
)

// WorkspaceValues is the Codefly query surface used by the host. The group is
// render-owned; a component cannot supply its own source.
type WorkspaceValues interface {
	WorkspaceValueIfSet(string, string) (string, bool, error)
}

// Configure selects the source the render handed this host, in the
// render-owned artifact-cache group: ROOT, the CLI's native artifact cache, in
// a local run outside a Pod; REPOSITORY, the repository a deployed render
// publishes every bundle to, in a deployed Pod, read as the workload's own
// identity. Neither is inferred: an absent group is no source, which keeps a
// frontend pending rather than serving a backend's mutable directory.
func Configure(ctx context.Context, values WorkspaceValues, local, inCluster bool) (Source, error) {
	root, rootSet, err := values.WorkspaceValueIfSet("artifact-cache", "ROOT")
	if err != nil {
		return nil, errors.New("read native frontend artifact source")
	}
	repository, repositorySet, err := values.WorkspaceValueIfSet("artifact-cache", "REPOSITORY")
	if err != nil {
		return nil, errors.New("read published frontend artifact source")
	}
	rootSet, repositorySet = rootSet && root != "", repositorySet && repository != ""
	switch {
	case rootSet && repositorySet:
		return nil, errors.New("artifact-cache names both a native ROOT and a published REPOSITORY; a host reads bundles from one source")
	case repositorySet:
		if local || !inCluster {
			return nil, errors.New("a published frontend artifact repository is read by a deployed Pod")
		}
		return NewWorkloadRegistrySource(repository)
	case !rootSet:
		return nil, nil
	}
	if !local || inCluster {
		return nil, errors.New("native frontend artifact cache requires a local non-pod run")
	}
	source, err := NewDirectorySource(root, inCluster)
	if err != nil {
		return nil, err
	}
	go func() { <-ctx.Done(); _ = source.Close() }()
	return source, nil
}
