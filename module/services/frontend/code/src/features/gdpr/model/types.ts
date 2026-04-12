/** Clean domain types for the GDPR / Data Privacy feature. */

export type GDPRRequestType = "export" | "deletion";

export type GDPRRequestStatus =
  | "pending"
  | "processing"
  | "completed"
  | "failed"
  | "cancelled";

export interface GDPRRequest {
  id: string;
  type: GDPRRequestType;
  status: GDPRRequestStatus;
  createdAt: string;
  completedAt: string | undefined;
  downloadUrl: string | undefined;
}
