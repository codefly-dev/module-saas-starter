package frontendpayload

import (
	"context"
	"errors"
)

// WorkspaceValues is the Codefly query surface used by the native run. The
// cache root is render-owned; a component cannot supply its own source.
type WorkspaceValues interface {
	WorkspaceValueIfSet(string, string) (string, bool, error)
}

func ConfigureNative(ctx context.Context, values WorkspaceValues, local, inCluster bool) (Source, error) {
	root, set, err := values.WorkspaceValueIfSet("artifact-cache", "ROOT")
	if err != nil {
		return nil, errors.New("read native frontend artifact source")
	}
	if !set || root == "" {
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
