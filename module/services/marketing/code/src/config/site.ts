import { publicSiteConfig } from "@/generated/public-site-config";

type PublicCustomer = {
  name: string;
  logo: string;
  url: string;
};

type PublicTestimonial = {
  quote: string;
  attribution: string;
  role: string;
};

export const siteConfig = {
  ...publicSiteConfig,
  customers: publicSiteConfig.customers as readonly PublicCustomer[],
  testimonials:
    publicSiteConfig.testimonials as readonly PublicTestimonial[],
};

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
