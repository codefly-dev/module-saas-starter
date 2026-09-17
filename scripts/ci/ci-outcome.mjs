#!/usr/bin/env node
// Render what a Codefly CI phase actually verified.
//
// `Codefly CI passed: 9 passed, 0 reused` is the whole story a reader gets
// today, and it leaves the two questions that decide whether a green check
// means anything: which services were *not* looked at, and which ones passed on
// evidence this run did not produce. A reused task is a task that did not run.
// A service outside the plan is a service nobody tested. Both are correct and
// both are invisible.
//
// The reasons matter as much as the counts. A task that passes but publishes
// nothing leaves every later run re-doing its work, and the only explanation
// lives in this report — Codefly records it per task as `cache.reason` and the
// workflow otherwise discards the file.
//
// Usage: ci-outcome.mjs <report.json>
// Writes a table to stdout and, when GitHub provides one, to the step summary.
import { readFileSync, appendFileSync } from 'node:fs';

const path = process.argv[2];
if (!path) {
  console.error('usage: ci-outcome.mjs <report.json>');
  process.exit(2);
}

let report;
try {
  report = JSON.parse(readFileSync(path, 'utf8'));
} catch (error) {
  // A phase that selected nothing writes no report, and a phase that died
  // before reporting is already failing on its own. Neither is this script's
  // failure to announce: it explains a run, it does not judge one.
  console.log(`No CI report at ${path} (${error.code ?? error.message}); nothing to report.`);
  process.exit(0);
}

const tasks = Array.isArray(report.tasks) ? report.tasks : [];
const service = task => task.resource ?? task.service ?? task.id ?? '(unknown)';
const seconds = task => (typeof task.duration_ms === 'number' ? `${(task.duration_ms / 1000).toFixed(1)}s` : '');

// Human age of a record, so "reused" carries how stale the evidence is.
function age(recordedAt) {
  if (!recordedAt) return 'unknown';
  const when = Date.parse(recordedAt);
  if (Number.isNaN(when)) return 'unknown';
  const minutes = Math.max(0, Math.round((Date.now() - when) / 60000));
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`;
}

// A task's cache status is how it reached its verdict: `hit` means a signed
// record stood in for execution, anything else means this run did the work.
const reused = tasks.filter(task => task.cache?.status === 'hit');
const executed = tasks.filter(task => task.cache?.status !== 'hit');

// A task that ran and passed but stored nothing is the silent failure mode:
// green here, and every later run repeats the work. `stored` is only ever true
// on a run allowed to publish, so a pull request never reports these.
const withheld = executed.filter(task =>
  task.status === 'passed' && task.cache && task.cache.stored === false && task.cache.reason);

const lines = [];
const out = line => lines.push(line);

out(`### ${report.phase ?? 'Codefly CI'} — what this verified`);
out('');
out(`**${executed.length} executed**, **${reused.length} reused**, ${tasks.length} task(s) in the plan.`);
out('');

if (executed.length) {
  out('| verified now | status | time |');
  out('| --- | --- | --- |');
  for (const task of executed) out(`| ${service(task)} | ${task.status ?? '?'} | ${seconds(task)} |`);
  out('');
}

if (reused.length) {
  out('| NOT run — reused prior evidence | signed by | recorded |');
  out('| --- | --- | --- |');
  for (const task of reused) {
    // Provenance of the record this task stood on. The age is the part a
    // reader judges: evidence from an hour ago on main reads very differently
    // from evidence from last week.
    const reuse = task.cache?.reuse ?? {};
    const from = [reuse.reference, reuse.run].filter(Boolean).join(' @ ') || 'a previously verified run';
    const revision = reuse.revision ? ` (${reuse.revision.slice(0, 8)})` : '';
    out(`| ${service(task)} | ${from}${revision} | ${age(reuse.recorded_at)} |`);
  }
  out('');
}

// Services the plan never selected: real coverage information, and the reason
// a reader most often wants ("did anything check the frontend?").
const skipped = Array.isArray(report.not_selected) ? report.not_selected : [];
if (skipped.length) {
  out('| NOT tested — outside this plan | why |');
  out('| --- | --- |');
  for (const entry of skipped) {
    out(`| ${entry.service ?? entry} | ${entry.reason ?? 'nothing it depends on changed'} |`);
  }
  out('');
}

if (withheld.length) {
  out('> **Results were not published.** Later runs will repeat this work.');
  out('');
  for (const task of withheld) out(`> - \`${service(task)}\`: ${task.cache.reason}`);
  out('');
}

const summary = lines.join('\n');
console.log(summary);
if (process.env.GITHUB_STEP_SUMMARY) {
  try {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${summary}\n`);
  } catch (error) {
    // The summary is a convenience; failing to write it must not fail a phase
    // whose work already succeeded.
    console.error(`could not write the step summary: ${error.message}`);
  }
}
