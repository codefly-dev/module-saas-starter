import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { GDPRService } from "@/gen/user_pb";

const client = createClient(GDPRService, apiTransport);

export const gdprQueries = {
  exportStatus: (id: string) =>
    queryOptions({
      queryKey: ["gdpr", "export", id],
      queryFn: () => client.getExportStatus({ id }),
      enabled: !!id,
    }),

  deletionStatus: (id: string) =>
    queryOptions({
      queryKey: ["gdpr", "deletion", id],
      queryFn: () => client.getDeletionStatus({ id }),
      enabled: !!id,
    }),
};
