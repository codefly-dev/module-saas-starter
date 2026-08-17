package business

// FeatureFlag represents one row in the legacy migration inventory.
type FeatureFlag struct {
	ID             string
	Name           string
	Description    string
	Enabled        bool
	RolloutPercent int
	TargetOrgIDs   []string
}
