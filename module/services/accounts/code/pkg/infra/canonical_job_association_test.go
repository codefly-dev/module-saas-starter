//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// Exercise every consumer of the shared SQL association predicate against real
// immutable queued jobs. Only the retained link is deliberately corrupted;
// attributes are fixed at creation. The normal lifecycle test separately proves
// terminal outcomes and acknowledgement. A malformed attempt is never claimed.
func assertCanonicalJobAssociations(t *testing.T, ctx context.Context, pending domain.Installation) {
	t.Helper()
	var original []byte
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT consent FROM public.authority_core_installations WHERE id=$1`, pending.ID).Scan(&original)
	}))
	defer func() {
		require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE public.authority_core_installations SET consent=$2::jsonb WHERE id=$1`, pending.ID, original)
			return err
		}))
	}()
	mutations := map[string]func(*jobsv1.NewJob){
		"organization": func(j *jobsv1.NewJob) {
			j.Scope.Value = &jobsv1.JobScope_OrganizationId{OrganizationId: business.NewIDString()}
		},
		"scope":            func(j *jobsv1.NewJob) { j.Scope.Value = &jobsv1.JobScope_Global{Global: true} },
		"queue":            func(j *jobsv1.NewJob) { j.Queue = "example-other-setup" },
		"source":           func(j *jobsv1.NewJob) { j.Source = "example.other" },
		"topic":            func(j *jobsv1.NewJob) { j.Topic = "example.other" },
		"canonical marker": func(j *jobsv1.NewJob) { j.Attributes["canonical.installation"] = "false" },
		"installation":     func(j *jobsv1.NewJob) { j.Attributes["codefly.installation_id"] = business.NewIDString() },
		"revision":         func(j *jobsv1.NewJob) { j.Attributes["codefly.installation_revision"] = "99999" },
		"operation":        func(j *jobsv1.NewJob) { j.Attributes["canonical.operation"] = business.NewIDString() },
		"intent":           func(j *jobsv1.NewJob) { j.Attributes["canonical.intent"] = "different intent" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			req, err := business.NewInstallationStepJob(business.InstallationStep{InstallationID: pending.ID, OrganizationID: pending.Organization, Revision: pending.Revision}, business.NewIDString())
			require.NoError(t, err)
			req.Job.Ordering = nil
			req.Job.Attributes["canonical.installation"] = "true"
			req.Job.Attributes["canonical.operation"] = pending.Consent.OperationID
			req.Job.Attributes["canonical.intent"] = pending.Consent.Intent
			mutate(req.Job)
			queued, err := infra.EnqueueCanonicalAssociationFixture(ctx, testStore, req.Job.Scope.GetOrganizationId(), req)
			require.NoError(t, err)
			require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
				id, err := json.Marshal(queued.JobId)
				if err != nil {
					return err
				}
				_, err = storetx.Tx(ctx).Exec(ctx, `UPDATE public.authority_core_installations SET consent=jsonb_set(consent,'{host_plan,SetupJobID}',$2::jsonb) WHERE id=$1`, pending.ID, id)
				return err
			}))
			for _, id := range []string{pending.ID, ""} {
				rows, err := testStore.CanonicalSolutionSummaries(ctx, pending.Organization, id, "", gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED, 20)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.Equal(t, "unavailable", rows[0].Installation.SetupState)
				require.Empty(t, rows[0].Installation.FailureReason)
			}
			_, err = testStore.EnqueueCanonicalInstallationStep(ctx, pending, true)
			require.Error(t, err, "replay must not accept a misassociated job")
			active := pending
			active.Status = domain.Active
			err = testStore.AuthorityCoreWithConsent(nil).Write(ctx, func(tx authority.Tx) error { return tx.PutInstallation(active) })
			require.ErrorContains(t, err, "exact setup job", "activation must not accept an unrelated acknowledgement")
		})
	}
}
