import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { SSOAdminService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(SSOAdminService, apiTransport);

export const ssoQueries = {
  // status(orgId) returns the OrgSSOConfig for an org. The api Get
  // returns an empty proto (provider="") for never-configured orgs
  // so we don't error on the FE's first visit — easier branching.
  status: (orgId: string) =>
    queryOptions({
      queryKey: ["sso", "status", orgId],
      queryFn: () => client.getSSO({ orgId }),
      enabled: !!orgId,
    }),
};
