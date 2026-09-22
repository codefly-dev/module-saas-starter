#!/usr/bin/env node
// commit-identity-gate — keep an employer domain out of the commits this repository publishes.
//
// The naming gate scans the tree. A commit carries two fields it never reads: the author and
// committer email. Those leaked the same consumer domain the tree forbids, across 506 of the 865
// commits on `main` as of 2026-09-14 — a measurement taken then, not a running total — and unlike
// a file, they cannot be scrubbed. Author and committer are part of the commit object, so changing
// one rewrites every descendant hash: it breaks every clone, fork and open pull request, and
// GitHub keeps serving the original objects until a Support request garbage-collects them.
// Rewriting is a disclosure decision, deliberately not taken here (CLAIM_INVENTORY.md records it).
// This gate is the other half — it stops the count growing.
//
//   node tools/commit-identity-gate.mjs check <base> [head]    # every commit in <base>..<head>, on a pull request
//   node tools/commit-identity-gate.mjs report <base> [head]   # the same range post-merge on main, as a report
//
// The rule is an ALLOWLIST, not a denylist of forbidden domains, and that is the whole point.
// A denylist only catches the domains someone remembered to list, so the next contributor's
// employer leaks exactly as this one did. GitHub hands every account a no-reply address that
// carries no domain at all, so requiring it is both stricter and easier to comply with:
//
//     git config user.email <id>+<login>@users.noreply.github.com
//
// It reports the commit, never the address. A rejected email is by definition one this
// repository should not publish, and CI logs here are public — printing it to explain the
// failure would publish it in the course of preventing its publication.
//
// Scoped to <base>..HEAD, so it judges only what a change proposes to add. History is out of
// reach by construction, which is what makes this the step with no blast radius.
//
// `check` cannot see the one identity it most needs to: the squash merge produces. GitHub takes a
// squash commit's AUTHOR from the merging account's profile email, not from the commit it squashes,
// so a branch whose every commit `check` passed still lands a personal address on `main` when that
// account has "Keep my email address private" switched off. That object does not exist until the
// merge is performed, after every required check has reported, so nothing on the pull request can
// catch it. `report` is that missing half: run on `push` over the range the push added, it cannot
// stop the commit — landed, and its identity can no longer be scrubbed — but it turns the growth
// the gate measures into a visible failure instead of a count that rises unseen.

import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);

// GitHub's own two identity forms, and nothing else. The per-account no-reply address covers
// humans, Dependabot and github-actions[bot] alike (`<id>+<login>@users.noreply.github.com`);
// the bare `noreply@github.com` is what GitHub commits as for a squash, a web edit and a merge
// queue entry, so rejecting it would fail the queue on its own merge commit.
const NOREPLY_SUFFIX = "@users.noreply.github.com";
const GITHUB_NOREPLY = "noreply@github.com";

const accepted = (email) => {
  const address = email.trim().toLowerCase();
  return address === GITHUB_NOREPLY || address.endsWith(NOREPLY_SUFFIX);
};

export function identityErrors(commits) {
  const errors = [];
  for (const { sha, author, committer } of commits) {
    // Both fields, separately: a rebase rewrites the committer and leaves the author untouched,
    // so a branch fixed by rebasing alone still publishes the original author address.
    for (const [field, email] of [["author", author], ["committer", committer]]) {
      if (!accepted(email ?? "")) errors.push(`commit ${sha.slice(0, 8)}: ${field} email is not a GitHub no-reply address`);
    }
  }
  return errors;
}

// %x1f separates the three fields and %x1e terminates each record. An email cannot contain
// either, and the record form stays stable if a field is ever added.
export function parseCommits(output) {
  return output
    .split("\x1e")
    .map((record) => record.replace(/^\n/, ""))
    .filter((record) => record.includes("\x1f"))
    .map((record) => {
      const [sha, author, committer] = record.split("\x1f");
      return { sha, author, committer };
    });
}

// Both ends of the range are load-bearing. The tip is the pull request's own head; the base is the
// CURRENT base-branch tip, which in CI is the first parent of the checked-out `refs/pull/N/merge`.
// `pull_request.base.sha` is NOT that tip: it is the base as of the pull request's last push, and
// it does not follow the base branch afterwards. Once the base moves, a re-run pairs that stale sha
// with a merge ref rebuilt against the newer base, and the range picks up every commit the base
// gained — squash merges included, which are ordinary commits `--no-merges` does not exclude. The
// pull request then fails on published work by other authors, outside its own author's power to
// rewrite. Reading the base off the merge ref drops the dependency on `base.sha` entirely.
//
// Merge commits are excluded because their identity is machinery, not authorship — and judging it
// rejects addresses no contributor can fix. GitHub authors the synthetic merge-ref commit with the
// BASE branch's identity, which on this history is a pre-existing one by definition.
export const logArgs = (base, tip = "HEAD") => [
  "log", "--no-merges", "--format=%H%x1f%ae%x1f%ce%x1e", `${base}..${tip}`,
];

function commits(base, tip) {
  try {
    return parseCommits(
      execFileSync("git", logArgs(base, tip), {
        encoding: "utf8",
        maxBuffer: 64 * 1024 * 1024,
        stdio: ["ignore", "pipe", "inherit"],
      }),
    );
  } catch {
    console.error(`commit-identity-gate: cannot read commits in ${base}..${tip ?? "HEAD"}`);
    process.exit(1);
  }
}

function check(base, tip) {
  const range = commits(base, tip);
  const errors = identityErrors(range);
  if (errors.length) {
    console.error("commit-identity-gate: a commit publishes an email address this repository cannot retract:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} identity field(s). The address is deliberately not printed — this ` +
        `log is public. Point your identity at the no-reply address GitHub issues you:\n\n` +
        `    git config user.email <id>+<login>@users.noreply.github.com\n\n` +
        `(find it under Settings → Emails → "Keep my email addresses private".) Then rewrite the ` +
        `commits already on the branch — a plain rebase fixes the committer but keeps the ` +
        `original author, so reset it explicitly:\n\n` +
        `    git rebase ${base} --exec 'git commit --amend --no-edit --reset-author'\n\n` +
        `See AGENTS.md §"Naming and confidentiality".`,
    );
    process.exit(1);
  }
  console.log(`✓ ${range.length} commit(s) carry a GitHub no-reply identity.`);
}

// The post-merge counterpart. Its remediation is not `check`'s — the commit is already on `main`
// and cannot be rewritten, so telling the contributor to rebase the branch would be advice for a
// thing that no longer exists. The address is still withheld: this log is public too.
function report(base, tip) {
  const range = commits(base, tip);
  const errors = identityErrors(range);
  if (errors.length) {
    console.error("commit-identity-gate: an email address this repository cannot retract has landed on main:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} identity field(s) already published on main. The commit is landed ` +
        `and its identity can no longer be scrubbed, so this reports the growth rather than ` +
        `preventing it. GitHub takes a squash commit's author from the merging account's profile ` +
        `email, so pointing user.email at the no-reply address is not enough on its own — also turn ` +
        `on Settings → Emails → "Keep my email addresses private" before the next merge.`,
    );
    process.exit(1);
  }
  console.log(`✓ ${range.length} commit(s) landed on main carry a GitHub no-reply identity.`);
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const [cmd, base, tip] = process.argv.slice(2);
  const run = { check, report }[cmd];
  if (!run || !base) {
    console.error("usage: commit-identity-gate.mjs <check|report> <base> [head]");
    process.exit(2);
  }
  run(base, tip);
}
