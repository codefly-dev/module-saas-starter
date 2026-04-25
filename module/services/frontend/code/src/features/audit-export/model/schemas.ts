import { z } from "zod";

// Form schema for the per-org audit-export config. Mirrors
// pkg/business/audit_export_s3.AuditExportConfig + the Save RPC's
// validation rules. The api also enforces these (cadence >= 5,
// bucket non-empty); duplicating here for snappy client-side errors.
export const auditExportFormSchema = z.object({
  bucket: z.string().min(1, "Bucket name is required"),
  region: z.string().min(1, "Region is required").default("us-east-1"),
  // endpoint is OPTIONAL — empty means real AWS S3 in `region`.
  // When set, may be:
  //   "host:port"           → TLS on  (production / S3-compat with TLS)
  //   "https://host:port"   → TLS on
  //   "http://host:port"    → TLS OFF (local MinIO, on-prem dev)
  // The api parses the scheme and toggles TLS accordingly.
  endpoint: z.string().optional().default(""),
  prefix: z.string().optional().default(""),
  accessKeyId: z.string().min(1, "Access key id is required"),
  // secretAccessKey is omitted on edit (the api Get returns "" so the
  // FE never displays a stored secret). Submitting "" means "preserve
  // existing"; submitting a value rotates it. Required only on the
  // first save.
  secretAccessKey: z.string().optional().default(""),
  cadenceMinutes: z
    .number()
    .int()
    .min(5, "Cadence must be >= 5 minutes")
    .max(10080, "Cadence must be <= 7 days"),
  enabled: z.boolean().default(true),
});

export type AuditExportFormValues = z.input<typeof auditExportFormSchema>;
