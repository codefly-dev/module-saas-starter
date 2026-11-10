package business

import "context"

// RetireInstallation runs the uninstall lifecycle UninstallSolution runs,
// without refusing a module's installation: what a module's withdrawal or an
// organization's deletion retires. For the tests of that lifecycle only; a
// person's uninstall of a module is refused (refuseModuleUninstall).
func (s *Service) RetireInstallation(ctx context.Context, actorID, orgID, installationID string) error {
	return s.narrow(ctx, func(ctx context.Context) error {
		return s.uninstallSolutionTx(ctx, actorID, s.actorTypeForCreator(ctx, actorID), orgID, installationID)
	})
}
