"""Exercise Accounts request-pool ownership on an isolated native PostgreSQL."""

import argparse
import os
from pathlib import Path
import socket
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--postgres-bin", type=Path, required=True)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    postgres = args.postgres_bin.resolve()
    env = {k: v for k, v in os.environ.items()
           if not k.startswith("PG") and k not in {"POSTGRES_TOKEN_FILE", "ACCOUNTS_DATABASE_TRANSPORT"}}
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
            env.update({"GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"})
            run([args.go, "test", "-race", "-count=1", "-v", "./qualification/scopedpools"],
                cwd=ROOT / "module/services/accounts/code")
        finally:
            run([postgres / "pg_ctl", "-D", data, "-m", "fast", "-w", "stop"])


if __name__ == "__main__":
    main()
