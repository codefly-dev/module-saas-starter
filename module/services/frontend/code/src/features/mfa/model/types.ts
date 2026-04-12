/** Clean domain types for the MFA feature. */

export type MFADeviceType = "totp" | "webauthn" | "sms";

export interface MFADevice {
  id: string;
  name: string;
  type: MFADeviceType;
  createdAt: string;
  lastUsedAt: string | undefined;
}
