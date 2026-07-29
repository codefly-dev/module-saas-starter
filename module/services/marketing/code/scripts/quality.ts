import { readFile } from "node:fs/promises";
import path from "node:path";
import {
  allPublicContent,
  contentKinds,
  parseContentFile,
} from "../src/lib/content";
import {
  marketingRouteInventory,
  metadataForRoute,
} from "../src/lib/page-renderer";
import { siteConfig } from "../src/config/site";

const documents = (
  await Promise.all(
    siteConfig.locales.enabled.map((locale) => allPublicContent(locale)),
  )
).flat();
const routes = await marketingRouteInventory();
const paths = new Set(routes.map((segments) => `/${segments.join("/")}`));
paths.add("/feed.xml");
paths.add("/robots.txt");
paths.add("/sitemap.xml");
const failures: string[] = [];
const canonicals = new Set<string>();
const titles = new Set<string>();
const descriptions = new Set<string>();
const routeTitles = new Set<string>();
const routeDescriptions = new Set<string>();

for (const segments of routes) {
  const canonical = `/${segments.join("/")}`;
  if (canonicals.has(canonical)) failures.push(`duplicate canonical ${canonical}`);
  canonicals.add(canonical);
  const metadata = await metadataForRoute(segments);
  if (routeTitles.has(metadata.title)) {
    failures.push(`duplicate page title ${metadata.title}`);
  }
  if (routeDescriptions.has(metadata.description)) {
    failures.push(`duplicate page description ${metadata.description}`);
  }
  routeTitles.add(metadata.title);
  routeDescriptions.add(metadata.description);
}

for (const document of documents) {
  if (titles.has(document.title)) failures.push(`duplicate title ${document.title}`);
  if (descriptions.has(document.description)) {
    failures.push(`duplicate description ${document.description}`);
  }
  titles.add(document.title);
  descriptions.add(document.description);
  if (/<[a-z][\s\S]*>/i.test(document.body)) {
    failures.push(`${document.sourcePath}: raw HTML is forbidden`);
  }
  let previousHeading = 1;
  for (const match of document.body.matchAll(/^(#{2,4})\s+/gm)) {
    const current = match[1].length;
    if (current > previousHeading + 1) {
      failures.push(`${document.sourcePath}: heading level jumps to h${current}`);
    }
    previousHeading = current;
  }
  for (const match of document.body.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
    const href = match[1];
    if (href.startsWith("/") && !href.startsWith("//")) {
      const pathname = href.split(/[?#]/, 1)[0];
      if (!paths.has(pathname)) {
        failures.push(`${document.sourcePath}: broken internal link ${href}`);
      }
    } else if (!/^https:\/\//.test(href) && !/^mailto:/.test(href)) {
      failures.push(`${document.sourcePath}: unsafe link ${href}`);
    }
  }
  parseContentFile(
    await readFile(document.sourcePath, "utf8"),
    path.relative(process.cwd(), document.sourcePath),
  );
}

for (const kind of contentKinds) {
  if (!documents.some((document) => document.type === kind)) {
    failures.push(`no published ${kind} content`);
  }
}

if (failures.length > 0) {
  throw new Error(`Marketing quality checks failed:\n${failures.join("\n")}`);
}

process.stdout.write(
  `Marketing quality checks passed for ${routes.length} routes and ${documents.length} documents.\n`,
);
