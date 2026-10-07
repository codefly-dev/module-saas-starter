---
name: land-a-pull-request
description: Get a pull request through this repository's merge queue, and diagnose one that will not land — a queue entry that never merges, a required check that never reports on `merge_group`, a naming-gate failure on the PR title/body/commit messages, or a rejected commit identity. Use after opening or updating a PR here, when a merge looks accepted but nothing happens, or when CI is red on a gate that is green on the pull request itself.
---

# Landing a pull request

`main` merges through a **GitHub merge queue**, not by pressing Merge. The queue
builds each entry as `main + the pull request` and runs the required checks
against that merged ref, so a PR never has to be rebased merely because `main`
moved — "require branches to be up to date" is deliberately off, since with a
queue it re-imposes the rebase race it was meant to replace.

## Verify queue membership after requesting a merge

`gh pr merge` enqueues when required checks have passed and enables auto-merge
otherwise. **A successful exit alone establishes neither.** Inspect the state:

```bash
PR_NUMBER=626 # replace with the pull request number
gh api graphql -f query='query($number:Int!){repository(owner:"codefly-dev",name:"module-saas-starter")
  {pullRequest(number:$number){id state autoMergeRequest{enabledAt} mergeQueueEntry{position state}}}}' \
  -F number="$PR_NUMBER" --jq '.data.repository.pullRequest'
```

`state: MERGED` confirms completion; a non-null `mergeQueueEntry` confirms queue
membership. An `autoMergeRequest` alone confirms neither. If the PR is open,
eligible, and has no queue entry, request enqueue explicitly with its id (this
can fail if requirements are unmet), then repeat the lookup:

```bash
PRID=$(gh api graphql -f query='query($number:Int!){repository(owner:"codefly-dev",name:"module-saas-starter")
  {pullRequest(number:$number){id}}}' -F number="$PR_NUMBER" \
  --jq '.data.repository.pullRequest.id') &&
gh api graphql -f query='mutation($id:ID!){enqueuePullRequest(input:{pullRequestId:$id})
  {mergeQueueEntry{position state}}}' -f id="$PRID"
```

## Every required check must run on `merge_group`

A required context that never reports leaves its entry queued until the queue
evicts it, and entries merge in order — so one missing context stalls every merge
in the repository. No pull request run shows it, because the same job is green
there. The `release-contract` gate checks publication dependencies and action
pins; it does **not** enforce merge-queue trigger coverage or compare required
check names with the live ruleset.

Whenever workflows or required checks change, read the effective required
contexts:

```bash
gh api repos/codefly-dev/module-saas-starter/rules/branches/main \
  --jq '.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context'
```

Compare each context with the reporting job's `name` (or job id when unnamed) in
`.github/workflows/`, including any matrix-expanded name. Check that its workflow
subscribes to `merge_group` and that job conditions and dependencies permit it to
report on that event. Confirm those contexts actually report on the queue entry's
merged commit; a green PR run or `release-contract` alone cannot establish this.
See [RELEASE_GATES.md § The contract
test](../../../RELEASE_GATES.md#the-contract-test) for the existing guard's scope.

## A naming failure on the records, not the tree

The record check enforces the root `AGENTS.md` naming rule over the pull
request's **title, body and commit messages**. Its CI log names the matching mode
and nothing else — not the term, not the line — because the log is public and so
is the record it points into. To see the line, run it locally:

```bash
node module/tools/naming-gate.mjs check            # the tree
node module/tools/naming-gate.mjs message <file>   # a title/body/message
```

A pushed commit message can no longer be edited, so fix a failure by amending or
rebasing rather than by adding a commit on top. Opt in to the local hook so a bad
message is rejected before it is published:

```bash
git config core.hooksPath scripts/hooks
```

Setting `core.hooksPath` replaces the hooks directory wholesale, so any hook
already in `.git/hooks` stops running until you unset it. The hook is the only
half that runs before publication — CI blocks the *merge*, by which point the
commit is already public and GitHub keeps prior revisions of an edited body.

### Fix the body BEFORE you push, or the same failure survives the fix

**Two** workflows run the record check, over the same terms, from different
triggers, and both read `github.event.pull_request.body` — the event payload,
frozen when the event fired, never the live body:

- `naming-records.yml` listens for `edited`, so editing the body fires a fresh
  event and that job turns green immediately.
- `ci.yml` takes `pull_request`'s default types (opened, synchronize, reopened),
  so its copy — a step of the **Base manifest integrity** job — keeps evaluating
  the body as it stood at the last push. It is deliberately not re-run on every
  description edit.

So fixing the body after pushing leaves you with `naming-records` green and
`Base manifest integrity` red **on a body that no longer exists**, which reads
like a flake and is not one. Edit the title and body first, push afterwards, and
one run clears both. If you have already pushed, the only way forward is another
push — re-running `ci.yml` replays the original payload and fails identically.

Reading the live body instead would be the wrong fix: the gate scans what was
actually published, and an edit does not retract it, because GitHub serves prior
revisions of an edited body through its API.

## A rejected commit identity

Every commit a pull request adds is checked for an author and committer email
inside GitHub's no-reply domains — something the tree scan cannot see and no
scrub can reach. Set it once, before your first commit:

```bash
git config user.email <id>+<login>@users.noreply.github.com
```

See [RELEASE_GATES.md § Commit
identity](../../../RELEASE_GATES.md#commit-identity) for the address to use and
how to rewrite a branch that predates the gate.
