package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func (s *Service) ListPublicPlans(ctx context.Context) ([]PublicPlan, string, error) {
	plans, err := s.store.ListPublicPlans(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list public plans: %w", err)
	}
	document, err := json.Marshal(plans)
	if err != nil {
		return nil, "", fmt.Errorf("marshal public plan revision: %w", err)
	}
	digest := sha256.Sum256(document)
	return plans, "sha256:" + hex.EncodeToString(digest[:]), nil
}
