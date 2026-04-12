import type { MFADeviceType } from "./types";

export function formatDeviceType(type: MFADeviceType): string {
  switch (type) {
    case "totp":
      return "Authenticator App";
    case "webauthn":
      return "Security Key";
    case "sms":
      return "SMS";
    default:
      return "Unknown";
  }
}
