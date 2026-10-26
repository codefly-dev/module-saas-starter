package adapters

import (
	"accounts/pkg/business"
	installationv1 "accounts/pkg/gen/host/installation/v1"
)

func installationApproval(wire *installationv1.ConsentApproval) (*business.InstallationApproval, error) {
	approval := &business.InstallationApproval{TargetID: wire.GetTargetId(), Generation: wire.GetGeneration(), ExpectedRevision: wire.GetExpectedRevision(), OperationID: wire.GetOperationId()}
	return approval, approval.Validate()
}
