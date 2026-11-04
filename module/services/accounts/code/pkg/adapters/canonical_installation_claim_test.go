package adapters

import (
	"encoding/json"
	"testing"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

func TestCanonicalSetupClaimKeepsPrincipalOutOfHostTargetAndScopeOutOfBody(t *testing.T) {
	p := business.SolutionInstallationPlan{InstallationID: "canonical-installation", Organization: "org", Revision: 7,
		Target: "example-alias", HostTargetID: "immutable-host-target", BindingID: "binding",
		Approval: domain.ConsentApproval{TargetID: "signed-audience-principal"}}
	claim := canonicalInstallationClaim(business.CanonicalInstallationDelivery{Plan: p, Token: "fresh-step-credential"},
		&jobsv1.JobEnvelope{AttemptCount: 2}, &jobsv1.JobLeaseReference{JobId: "job", WorkerId: "worker", LeaseToken: "lease"})
	if claim["target_id"] != p.HostTargetID || claim["binding_id"] != p.BindingID || claim["route_alias"] != p.Target {
		t.Fatal("routing identities were inferred from the credential audience")
	}
	raw, err := json.Marshal(claim["installation"])
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 || string(body["installation_id"]) != `"canonical-installation"` || string(body["organization_id"]) != `"org"` || string(body["revision"]) != "7" {
		t.Fatalf("setup receiver got a different wire contract: %s", raw)
	}
	if claim["work_context"] != "fresh-step-credential" || claim["lease_token"] != "lease" {
		t.Fatal("the fresh credential or current lease was lost")
	}
}
