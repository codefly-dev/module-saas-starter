import type { MetadataRoute } from "next";
import { siteConfig } from "@/config/site";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: siteConfig.company.productName,
    short_name: siteConfig.company.productName,
    description: siteConfig.company.shortDescription,
    start_url: "/",
    display: "browser",
    background_color: siteConfig.brand.colors.background,
    theme_color: siteConfig.brand.colors.primary,
  };
}
