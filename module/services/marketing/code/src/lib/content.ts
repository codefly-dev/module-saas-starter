import { cache } from "react";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { parse as parseYAML } from "yaml";
import { z } from "zod";

export const contentKinds = [
  "article",
  "changelog",
  "doc",
  "legal",
  "use-case",
] as const;
export type ContentKind = (typeof contentKinds)[number];

const contentMetadataSchema = z
  .object({
    type: z.enum(contentKinds),
    slug: z.string().regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/),
    title: z.string().trim().min(1).max(100),
    description: z.string().trim().min(1).max(180),
    locale: z.string().min(2).max(35),
    state: z.enum(["draft", "scheduled", "published"]),
    revision: z.string().trim().min(1).max(80),
    author: z.string().trim().min(1).max(80),
    reviewer: z.string().trim().min(1).max(80),
    publishedAt: z.string().datetime(),
    updatedAt: z.string().datetime(),
    tags: z.array(z.string().regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/)).default([]),
    navigationOrder: z.number().int().min(0).max(10000).default(100),
    category: z.string().regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/).optional(),
    releaseId: z.string().trim().min(1).max(80).optional(),
    affectedFeatures: z
      .array(z.string().trim().min(1).max(80))
      .default([]),
  })
  .strict();

export type ContentMetadata = z.infer<typeof contentMetadataSchema>;
export type ContentDocument = ContentMetadata & {
  body: string;
  sourcePath: string;
};

export interface ContentProvider {
  list(kind: ContentKind, locale: string): Promise<ContentDocument[]>;
  get(
    kind: ContentKind,
    slug: string,
    locale: string,
  ): Promise<ContentDocument | null>;
  searchDocs(query: string, locale: string): Promise<ContentDocument[]>;
  tags(kind: ContentKind, locale: string): Promise<string[]>;
  authors(locale: string): Promise<string[]>;
}

function contentRoot(): string {
  return path.join(process.cwd(), "content");
}

export function parseContentFile(
  document: string,
  sourcePath: string,
  now = new Date(),
): ContentDocument {
  const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/.exec(document);
  if (!match) throw new Error(`${sourcePath}: missing YAML front matter`);
  const metadata = contentMetadataSchema.parse(parseYAML(match[1]));
  const publishedAt = new Date(metadata.publishedAt);
  if (metadata.state === "scheduled" && publishedAt <= now) {
    throw new Error(
      `${sourcePath}: scheduled publication is due; publish it through the content job`,
    );
  }
  if (new Date(metadata.updatedAt) < publishedAt) {
    throw new Error(`${sourcePath}: updatedAt cannot precede publishedAt`);
  }
  return { ...metadata, body: match[2].trim(), sourcePath };
}

const loadDocuments = cache(async (): Promise<ContentDocument[]> => {
  const directories = await readdir(contentRoot(), { withFileTypes: true });
  const documents: ContentDocument[] = [];
  for (const directory of directories
    .filter((entry) => entry.isDirectory())
    .sort((left, right) => left.name.localeCompare(right.name))) {
    const directoryPath = path.join(contentRoot(), directory.name);
    const files = (await readdir(directoryPath))
      .filter((name) => name.endsWith(".md"))
      .sort();
    for (const file of files) {
      const sourcePath = path.join(directoryPath, file);
      documents.push(
        parseContentFile(await readFile(sourcePath, "utf8"), sourcePath),
      );
    }
  }
  const identities = new Set<string>();
  for (const document of documents) {
    const identity = `${document.type}:${document.locale}:${document.slug}`;
    if (identities.has(identity)) {
      throw new Error(`Duplicate content identity ${identity}`);
    }
    identities.add(identity);
  }
  return documents;
});

function publicDocuments(
  documents: ContentDocument[],
  locale: string,
): ContentDocument[] {
  return documents
    .filter(
      (document) =>
        document.locale === locale && document.state === "published",
    )
    .sort(
      (left, right) =>
        left.navigationOrder - right.navigationOrder ||
        right.publishedAt.localeCompare(left.publishedAt) ||
        left.slug.localeCompare(right.slug),
    );
}

export const repositoryContentProvider: ContentProvider = {
  async list(kind, locale) {
    return publicDocuments(await loadDocuments(), locale).filter(
      (document) => document.type === kind,
    );
  },
  async get(kind, slug, locale) {
    return (
      (await this.list(kind, locale)).find(
        (document) => document.slug === slug,
      ) ?? null
    );
  },
  async searchDocs(query, locale) {
    const terms = query
      .toLocaleLowerCase(locale)
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 8);
    if (terms.length === 0) return [];
    return (await this.list("doc", locale)).filter((document) => {
      const searchable =
        `${document.title} ${document.description} ${document.body}`.toLocaleLowerCase(
          locale,
        );
      return terms.every((term) => searchable.includes(term));
    });
  },
  async tags(kind, locale) {
    return [
      ...new Set(
        (await this.list(kind, locale)).flatMap((document) => document.tags),
      ),
    ].sort();
  },
  async authors(locale) {
    return [
      ...new Set(
        publicDocuments(await loadDocuments(), locale).map(
          (document) => document.author,
        ),
      ),
    ].sort();
  },
};

export async function allPublicContent(
  locale: string,
): Promise<ContentDocument[]> {
  const lists = await Promise.all(
    contentKinds.map((kind) => repositoryContentProvider.list(kind, locale)),
  );
  return lists.flat();
}
