"""Exercise Accounts request-pool ownership on an isolated native PostgreSQL.

The suite this drives is gated on ACCOUNTS_SCOPED_POOL_TEST_DSN, which only this
script exports: without the fixture every case calls t.Skip and `go test` still
exits 0. A run that skips proves nothing, so this script reads the verbose
output back and fails unless every test in the package ran and passed. The
required inventory is derived from the package source rather than written down
here, so adding a test extends the requirement automatically and removing one is
visible in the diff instead of silently shrinking what CI proves.
"""

import argparse
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = ROOT / "module/services/accounts/code/qualification/scopedpools"

TEST_FUNCTION = re.compile(r"^func (Test\w+)\(t \*testing\.T\) \{", re.MULTILINE)


def declared_tests():
    """Every top-level test in the package, from its source."""
    declared = set()
    for source in sorted(PACKAGE.glob("*_test.go")):
        declared.update(TEST_FUNCTION.findall(source.read_text()))
    if not declared:
        raise SystemExit(f"no tests found in {PACKAGE}: the qualification package is empty or moved")
    return declared


def require_every_test_ran(output, declared):
    """Fail unless each declared test reported PASS, and nothing skipped."""
    passed = set(re.findall(r"^--- PASS: (Test\w+)", output, re.MULTILINE))
    skipped = sorted(set(re.findall(r"^\s*--- SKIP: ([\w/]+)", output, re.MULTILINE)))
    missing = sorted(declared - passed)
    problems = []
    if skipped:
        problems.append("skipped, so the fixture did not reach them: " + ", ".join(skipped))
    if missing:
        problems.append("declared but did not pass: " + ", ".join(missing))
    if problems:
        raise SystemExit("scoped-pool qualification did not prove what it claims; "
                         + "; ".join(problems))
    print(f"scoped-pool qualification: {len(passed)} declared tests ran and passed, 0 skipped")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--postgres-bin", type=Path, required=True)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    postgres = args.postgres_bin.resolve()
    env = {k: v for k, v in os.environ.items()
           if not k.startswith("PG") and k not in {"POSTGRES_TOKEN_FILE", "POSTGRES_TOKEN_FILES", "ACCOUNTS_DATABASE_TRANSPORT"}}
    with tempfile.TemporaryDirectory(prefix="scoped-pools-") as temporary:
        fixture = Path(temporary)
        data = fixture / "data"
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]

        def run(command, **kwargs):
            return subprocess.run([str(v) for v in command], check=True, env=env,
                                  timeout=120, **kwargs)

        run([postgres / "initdb", "-D", data, "-U", "postgres", "-A", "trust", "--no-locale"],
            stdout=subprocess.DEVNULL)
        # Bootstrap only uses the postgres fixture owner. Application logins must
        # authenticate, so reusing a stale projected token really fails.
        (data / "pg_hba.conf").write_text(
            "host all postgres 127.0.0.1/32 trust\n"
            "host all all 127.0.0.1/32 scram-sha-256\n")
        run([postgres / "pg_ctl", "-D", data, "-l", fixture / "postgres.log",
             "-o", f"-h 127.0.0.1 -p {port} -k '' -c password_encryption=scram-sha-256", "-w", "start"])
        try:
            run([postgres / "createdb", "-h", "127.0.0.1", "-p", str(port), "-U", "postgres", "scoped_pools_fixture"])
            env["ACCOUNTS_SCOPED_POOL_TEST_DSN"] = (
                f"postgres://postgres@127.0.0.1:{port}/scoped_pools_fixture?sslmode=disable")
            declared = declared_tests()
            # GOPROXY is off below, so the module cache must already hold
            # everything the package needs; warm it while the network is still
            # allowed rather than failing the offline run on a missing module.
            subprocess.run([args.go, "mod", "download"], check=True, timeout=600,
                           cwd=ROOT / "module/services/accounts/code",
                           env={**env, "GOWORK": "off", "GOTOOLCHAIN": "local"})
            env.update({"GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"})
            completed = subprocess.run(
                [args.go, "test", "-race", "-count=1", "-v", "./qualification/scopedpools"],
                check=False, env=env, cwd=ROOT / "module/services/accounts/code",
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=1800)
            sys.stdout.write(completed.stdout)
            sys.stdout.flush()
            if completed.returncode != 0:
                raise SystemExit(f"scoped-pool qualification failed (go test exit {completed.returncode})")
            require_every_test_ran(completed.stdout, declared)
        finally:
            run([postgres / "pg_ctl", "-D", data, "-m", "fast", "-w", "stop"])


if __name__ == "__main__":
    main()
