import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

test("the image exposes strict production readiness instead of forcing it off", async () => {
  const service = parse(await readFile("../service.codefly.yaml", "utf8"));
  assert.equal(service.spec["build-args"].MARKETING_STRICT_READINESS, "");
  const dockerfile = await readFile("../builder/Dockerfile", "utf8");
  assert.match(dockerfile, /^ARG MARKETING_STRICT_READINESS$/m);
  assert.match(
    dockerfile,
    /^ENV MARKETING_STRICT_READINESS=\$(?:\{MARKETING_STRICT_READINESS\}|MARKETING_STRICT_READINESS)$/m,
  );
  assert.doesNotMatch(dockerfile, /^ENV MARKETING_STRICT_READINESS=0$/m);
});

test("the Codefly image build context includes the workspace package root", async () => {
  const packages = await stat("packages");
  assert.equal(packages.isDirectory(), true);
});
