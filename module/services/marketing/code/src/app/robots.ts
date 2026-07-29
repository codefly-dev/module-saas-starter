import type { MetadataRoute } from "next";
import { marketingEnvironment } from "@/config/environment";
import { siteOrigin } from "@/config/site";

export default function robots(): MetadataRoute.Robots {
  if (!marketingEnvironment().indexable) {
    return {
      rules: { userAgent: "*", disallow: "/" },
      sitemap: `${siteOrigin}/sitemap.xml`,
    };
  }
  return {
    rules: [
      {
        userAgent: "*",
        allow: "/",
        disallow: ["/api/", "/maintenance"],
      },
    ],
    sitemap: `${siteOrigin}/sitemap.xml`,
    host: siteOrigin,
  };
}
