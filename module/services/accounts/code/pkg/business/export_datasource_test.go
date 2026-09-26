package business

import "context"

// AddExistingSource stores a source the way AddSource did before its provider
// stopped admitting new sources, so a test can exercise what the host still
// does for a source that already exists: sync it, refresh its credential,
// validate what it was connected with. It is compiled into tests only;
// production has no path around admission.
func (s *Service) AddExistingSource(ctx context.Context, actorID string, input AddSourceInput) (*DatasourceSource, error) {
	return s.addSource(ctx, actorID, input)
}
