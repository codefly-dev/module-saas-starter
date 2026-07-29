import type { MetadataRoute } from "next";
import { siteOrigin } from "@/config/site";
import { marketingRouteInventory } from "@/lib/page-renderer";

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const now = new Date();
  return (await marketingRouteInventory())
    .filter((segments) => segments[0] !== "maintenance")
    .map((segments) => ({
      url: new URL(`/${segments.join("/")}`, siteOrigin).toString(),
      lastModified: now,
      changeFrequency:
        segments.length === 0 ? ("weekly" as const) : ("monthly" as const),
      priority: segments.length === 0 ? 1 : 0.7,
    }));
}
