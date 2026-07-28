import { siteConfig, siteOrigin } from "@/config/site";
import { repositoryContentProvider } from "@/lib/content";

function escapeXML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&apos;");
}

export async function GET() {
  const entries = [
    ...(
      await repositoryContentProvider.list(
        "article",
        siteConfig.locales.default,
      )
    ).map((document) => ({
      ...document,
      href: `/blog/${document.slug}`,
    })),
    ...(
      await repositoryContentProvider.list(
        "changelog",
        siteConfig.locales.default,
      )
    ).map((document) => ({
      ...document,
      href: `/changelog/${document.slug}`,
    })),
  ].sort((left, right) => right.publishedAt.localeCompare(left.publishedAt));
  const items = entries
    .map(
      (entry) => `<item>
  <title>${escapeXML(entry.title)}</title>
  <description>${escapeXML(entry.description)}</description>
  <link>${escapeXML(new URL(entry.href, siteOrigin).toString())}</link>
  <guid>${escapeXML(`${entry.type}:${entry.revision}:${entry.slug}`)}</guid>
  <pubDate>${new Date(entry.publishedAt).toUTCString()}</pubDate>
</item>`,
    )
    .join("\n");
  const body = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
  <title>${escapeXML(siteConfig.company.productName)}</title>
  <description>${escapeXML(siteConfig.company.shortDescription)}</description>
  <link>${escapeXML(siteOrigin)}</link>
${items}
</channel>
</rss>
`;
  return new Response(body, {
    headers: {
      "Cache-Control": "public, max-age=300, stale-while-revalidate=3600",
      "Content-Type": "application/rss+xml; charset=utf-8",
    },
  });
}
