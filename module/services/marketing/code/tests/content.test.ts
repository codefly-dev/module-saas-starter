import assert from "node:assert/strict";
import test from "node:test";
import {
  allPublicContent,
  parseContentFile,
  repositoryContentProvider,
} from "@/lib/content";

test("publishes every content kind with unique identities", async () => {
  const documents = await allPublicContent();
  assert.ok(documents.length >= 5);
  const identities = documents.map(
    (document) => `${document.type}:${document.locale}:${document.slug}`,
  );
  assert.equal(new Set(identities).size, identities.length);
  assert.ok(documents.every((document) => document.state === "published"));
});

test("searches only published documentation", async () => {
  const results = await repositoryContentProvider.searchDocs("deployment");
  assert.ok(results.some((document) => document.slug === "deployment-topology"));
  assert.ok(results.every((document) => document.type === "doc"));
});

test("rejects unknown front matter and due scheduled content", () => {
  const frontMatter = `---
type: article
slug: example
title: Example
description: Example description
locale: en
state: scheduled
revision: v1
author: Author
reviewer: Reviewer
publishedAt: 2026-01-01T00:00:00.000Z
updatedAt: 2026-01-01T00:00:00.000Z
tags: []
navigationOrder: 1
affectedFeatures: []
unknown: unsafe
---
Body`;
  assert.throws(() => parseContentFile(frontMatter, "example.md"), /unrecognized key/i);
  assert.throws(
    () => parseContentFile(frontMatter.replace("unknown: unsafe\n", ""), "example.md"),
    /scheduled publication is due/,
  );
});
