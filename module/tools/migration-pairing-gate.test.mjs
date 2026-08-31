import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { migrationPairingErrors } from "./migration-pairing-gate.mjs";

// Build a throwaway module tree whose services each own the given migration
// filenames, run the gate against it, and clean up.
function withMigrations(services, body) {
  const root = mkdtempSync(join(tmpdir(), "migration-pairing-"));
  try {
    for (const [service, files] of Object.entries(services)) {
      const dir = join(root, "services", service, "migrations");
      mkdirSync(dir, { recursive: true });
      for (const name of files) writeFileSync(join(dir, name), "SELECT 1;");
    }
    return body(root);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

test("the shipped migrations satisfy the gate", () => {
  assert.deepEqual(migrationPairingErrors(join(import.meta.dirname, "..")), []);
});

test("matched up + down pairs pass", () => {
  withMigrations(
    { store: ["1_a.up.sql", "1_a.down.sql", "2_b.up.sql", "2_b.down.sql"] },
    (root) => assert.deepEqual(migrationPairingErrors(root), []),
  );
});

test("an up file with no down is an orphan", () => {
  withMigrations({ store: ["1_a.up.sql", "1_a.down.sql", "2_b.up.sql"] }, (root) => {
    const errors = migrationPairingErrors(root);
    assert.equal(errors.length, 1);
    assert.match(errors[0], /store: migration version 2 \(b\) has an \.up\.sql but no \.down\.sql/);
  });
});

test("a down file with no up is an orphan", () => {
  withMigrations({ store: ["1_a.down.sql"] }, (root) => {
    const errors = migrationPairingErrors(root);
    assert.equal(errors.length, 1);
    assert.match(errors[0], /store: migration version 1 \(a\) has a \.down\.sql but no \.up\.sql/);
  });
});

test("two names sharing one version is a duplicate", () => {
  withMigrations(
    { store: ["1_a.up.sql", "1_a.down.sql", "1_b.up.sql", "1_b.down.sql"] },
    (root) => {
      const errors = migrationPairingErrors(root);
      assert.equal(errors.length, 1);
      assert.match(errors[0], /store: migration version 1 is duplicated across a, b/);
    },
  );
});

test("each service is checked independently", () => {
  withMigrations(
    {
      store: ["1_a.up.sql", "1_a.down.sql"],
      jobs: ["1_x.up.sql"],
    },
    (root) => {
      const errors = migrationPairingErrors(root);
      assert.equal(errors.length, 1);
      assert.match(errors[0], /jobs: migration version 1/);
    },
  );
});

test("non-migration files are ignored", () => {
  withMigrations(
    { store: ["1_a.up.sql", "1_a.down.sql", "README.md", "scratch.sql"] },
    (root) => assert.deepEqual(migrationPairingErrors(root), []),
  );
});

test("returns nothing when no service ships migrations", () => {
  withMigrations({}, (root) => {
    mkdirSync(join(root, "services"), { recursive: true });
    assert.deepEqual(migrationPairingErrors(root), []);
  });
});
