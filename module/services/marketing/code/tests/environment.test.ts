import assert from "node:assert/strict";
import test from "node:test";
import { marketingEnvironment } from "@/config/environment";

test("accepts a public-only environment", () => {
  assert.deepEqual(
    marketingEnvironment({
      MARKETING_ENABLED: "true",
      MARKETING_INDEXABLE: "false",
      MARKETING_CATALOG_URL: "https://api.example.test",
      MARKETING_RELEASE: "release-1",
    }),
    {
      enabled: true,
      indexable: false,
      catalogURL: "https://api.example.test",
      release: "release-1",
    },
  );
});

test("rejects credential-bearing and insecure public URLs", () => {
  assert.throws(
    () =>
      marketingEnvironment({
        MARKETING_CATALOG_URL: "https://user:password@example.test",
      }),
    /Invalid public marketing environment/,
  );
  assert.throws(
    () =>
      marketingEnvironment({
        MARKETING_CATALOG_URL: "http://api.example.test",
      }),
    /HTTPS outside localhost/,
  );
});
