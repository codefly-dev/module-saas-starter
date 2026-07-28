import assert from "node:assert/strict";
import test from "node:test";
import { authorSlug, localizedPath } from "@/lib/page-renderer";

test("localized routes keep the default unprefixed and prefix other locales", () => {
  assert.equal(localizedPath(["docs", "guide"], "en", "en"), "/docs/guide");
  assert.equal(localizedPath(["docs", "guide"], "fr", "en"), "/fr/docs/guide");
});

test("author routes use the same canonical slug for generation and lookup", () => {
  assert.equal(authorSlug("Jean-Luc Picard"), "jean-luc-picard");
  assert.equal(authorSlug("  Grace Hopper  "), "grace-hopper");
});
