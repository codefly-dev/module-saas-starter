import assert from "node:assert/strict";
import test from "node:test";

import { interfaceDocsErrors } from "./interface-docs-gate.mjs";

const method = (overrides = {}) => ({
  procedure: "/saas.accounts.v1.TeamService/CreateTeam",
  service: "TeamService",
  method: "CreateTeam",
  description: "Create a team inside the caller's organization.",
  policy: { exposure: "EXPOSURE_AUTHENTICATED" },
  ...overrides,
});

const catalog = (...methods) => ({ methods });

test("a readable public summary passes", () => {
  assert.deepEqual(interfaceDocsErrors(catalog(method())), []);
});

test("an operation with no description fails", () => {
  const errors = interfaceDocsErrors(catalog(method({ description: "" })));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /has no description/);
});

test("a summary that names an action but not its object fails", () => {
  const errors = interfaceDocsErrors(catalog(method({ description: "Delete one." })));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /does not name what it acts on/);
});

test("an unpunctuated or lowercase description fails", () => {
  const errors = interfaceDocsErrors(
    catalog(method({ description: "create a team inside the caller's organization" })),
  );
  assert.equal(errors.length, 2);
  assert.match(errors[0], /does not end in a period/);
  assert.match(errors[1], /does not start with a capital/);
});

test("only the first sentence has to stand alone", () => {
  const description =
    "List the organization's API keys. Page with page_size (max 100) and page_token; revoked keys are included.";
  assert.deepEqual(interfaceDocsErrors(catalog(method({ description }))), []);
});

test("an over-long first sentence fails", () => {
  const description = `Create ${"a very deliberate team ".repeat(12)}.`;
  const errors = interfaceDocsErrors(catalog(method({ description })));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /move the detail to a later sentence/);
});

test("signature shorthand fails", () => {
  const errors = interfaceDocsErrors(
    catalog(method({ description: "Resolve plaintext key -> key and org id." })),
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /shorthand a non-engineer cannot read/);
});

test("an audience marker in the summary fails", () => {
  const errors = interfaceDocsErrors(
    catalog(method({ description: "Internal: resolve a key for the gateway." })),
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /opens with an audience marker/);
});

test("internal operations only have to be documented", () => {
  const internal = method({
    procedure: "/saas.accounts.v1.APIKeyService/ValidateAPIKey",
    description: "Internal: plaintext key -> key and org id.",
    policy: { exposure: "EXPOSURE_INTERNAL" },
  });
  assert.deepEqual(interfaceDocsErrors(catalog(internal)), []);
  const undocumented = interfaceDocsErrors(catalog(method({ ...internal, description: "" })));
  assert.equal(undocumented.length, 1);
  assert.match(undocumented[0], /has no description/);
});

test("two public operations may not share a summary", () => {
  const errors = interfaceDocsErrors(
    catalog(
      method(),
      method({ procedure: "/saas.accounts.v1.TeamService/UpdateTeam" }),
    ),
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /share the summary/);
});

test("an operation in an unplaced service fails", () => {
  const errors = interfaceDocsErrors(
    catalog(method({ procedure: "/saas.accounts.v1.LedgerService/Post", service: "LedgerService" })),
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /belongs to no bounded context/);
});

test("a module-facing capability must be placed method by method", () => {
  const placed = method({
    procedure: "/saas.accounts.v1.ModuleCapabilitiesService/AckJob",
    service: "ModuleCapabilitiesService",
    method: "AckJob",
    description: "Acknowledge a leased job as complete.",
    policy: { exposure: "EXPOSURE_INTERNAL" },
  });
  assert.deepEqual(interfaceDocsErrors(catalog(placed)), []);
  const errors = interfaceDocsErrors(catalog({ ...placed, method: "AckWidget" }));
  assert.equal(errors.length, 1);
  assert.match(errors[0], /belongs to no bounded context/);
});

test("an empty catalog fails rather than passing vacuously", () => {
  assert.deepEqual(interfaceDocsErrors(catalog()), ["the service catalog carries no methods"]);
});
