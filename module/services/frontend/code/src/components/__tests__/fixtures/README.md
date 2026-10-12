# Fixtures

`deleted-solution-dashboard.txt` is `src/solutions/SolutionDashboard.tsx` as it
stood at `8590d329`, before #1041 replaced its hand-rolled widget dispatch with
a mount of the kit's renderer. The duplication gate asserts that it would be
refused today, so the rule is pinned against the real duplication rather than a
paraphrase of it.

It is committed rather than read with `git show` because the suite ships inside
an immutable module package: a consuming repository carries this source tree and
not this repository's history, so a fixture that reaches for an upstream commit
fails there with `fatal: not a git repository` while passing in full-history CI
— a portability failure that CI is structurally unable to see.

The `.txt` extension is deliberate. The file is a byte fixture, not a module:
as `.tsx` it would be typechecked against today's imports, linted, and counted
by `ui-inventory.json`.
