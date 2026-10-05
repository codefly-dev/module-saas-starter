import assert from "node:assert/strict";
import test from "node:test";

import nextConfig from "@/../next.config.mjs";

type Header = { key: string; value: string };

async function headersFor(path: string): Promise<Map<string, string>> {
  const groups = await nextConfig.headers!();
  const matched = groups.filter((group) => group.source.startsWith("/:path"));
  assert.ok(matched.length > 0, `no header group matches ${path}`);
  return new Map(
    matched.flatMap((group) => group.headers as Header[]).map((h) => [h.key, h.value]),
  );
}

// The edge redirects plaintext to TLS, which protects the redirect and nothing before
// it: a browser's first request to a host it has never pinned goes out in the clear.
// The origin has to ask the browser to remember.
test("the marketing origin pins HTTPS", async () => {
  const headers = await headersFor("/");
  const sts = headers.get("Strict-Transport-Security");
  assert.ok(sts, "Strict-Transport-Security is absent");
  assert.match(sts, /max-age=\d+/);
  assert.ok(Number(sts.match(/max-age=(\d+)/)![1]) >= 31536000, "max-age is under a year");
});

// includeSubDomains and preload commit hosts this site does not own: a sibling
// subdomain on plaintext breaks under the first, and the second is effectively
// irreversible. Their absence is a decision, so it is asserted.
test("the marketing origin commits no host but its own", async () => {
  const sts = (await headersFor("/")).get("Strict-Transport-Security")!;
  assert.ok(!sts.includes("includeSubDomains"));
  assert.ok(!sts.includes("preload"));
});

// The framing and sniffing defenses beside it, so removing any one of the three is
// detected rather than only the newest.
test("the marketing origin refuses framing and sniffing", async () => {
  const headers = await headersFor("/");
  assert.equal(headers.get("X-Frame-Options"), "DENY");
  assert.equal(headers.get("X-Content-Type-Options"), "nosniff");
  assert.match(headers.get("Content-Security-Policy")!, /frame-ancestors 'none'/);
});
