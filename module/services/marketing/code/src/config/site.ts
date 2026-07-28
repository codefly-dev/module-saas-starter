import { publicSiteConfig } from "@/generated/public-site-config";

export const siteConfig = publicSiteConfig;

export const siteOrigin = new URL(siteConfig.urls.marketing).origin;
export const appOrigin = new URL(siteConfig.urls.app).origin;

export type AcquisitionMode =
  | "open_signup"
  | "waitlist"
  | "invite_only"
  | "request_demo";

export function acquisitionMode(): AcquisitionMode {
  return siteConfig.acquisition.mode;
}
