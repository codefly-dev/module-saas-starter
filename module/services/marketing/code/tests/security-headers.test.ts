import assert from "node:assert/strict";
import test from "node:test";

import nextConfig from "@/../next.config.mjs";

async function headersFor(path: string): Promise<Map<string, string>> {
  const groups = await nextConfig.headers!();
  const matched = groups.filter((group) => group.source.startsWith("/:path"));
  assert.ok(matched.length > 0, `no header group matches ${path}`);
  return new Map(matched.flatMap((group) => group.headers).map((h) => [h.key, h.value]));
}

// A header value is a list of directives, so a directive is only present when it stands
// as its own list member. Searching the whole string for "max-age=" accepts
// "not-max-age=31536000", which pins nothing: the assertions below read the parsed list.
function directives(value: string): Map<string, string> {
  return new Map(
    value.split(";").map((part) => {
      const [name, ...rest] = part.trim().split("=");
      return [name.toLowerCase(), rest.join("=")];
    }),
  );
}

// The edge redirects plaintext to TLS, which protects the redirect and nothing before
// it: a browser's first request to a host it has never pinned goes out in the clear.
// The origin has to ask the browser to remember.
test("the marketing origin pins HTTPS", async () => {
  const sts = (await headersFor("/")).get("Strict-Transport-Security");
  assert.ok(sts, "Strict-Transport-Security is absent");
  const maxAge = directives(sts).get("max-age");
  assert.ok(maxAge !== undefined, `max-age is not a directive of ${sts}`);
  assert.match(maxAge, /^\d+$/);
  assert.ok(Number(maxAge) >= 31536000, "max-age is under a year");
});

// includeSubDomains and preload commit hosts this site does not own: a sibling
// subdomain on plaintext breaks under the first, and the second is effectively
// irreversible. Their absence is a decision, so it is asserted.
test("the marketing origin commits no host but its own", async () => {
  const sts = (await headersFor("/")).get("Strict-Transport-Security")!;
  const names = [...directives(sts).keys()];
  assert.ok(!names.includes("includesubdomains"), `includeSubDomains is set in ${sts}`);
  assert.ok(!names.includes("preload"), `preload is set in ${sts}`);
});

// The framing and sniffing defenses beside it, so removing any one of the three is
// detected rather than only the newest.
test("the marketing origin refuses framing and sniffing", async () => {
  const headers = await headersFor("/");
  assert.equal(headers.get("X-Frame-Options"), "DENY");
  assert.equal(headers.get("X-Content-Type-Options"), "nosniff");
  assert.match(headers.get("Content-Security-Policy")!, /frame-ancestors 'none'/);
});
