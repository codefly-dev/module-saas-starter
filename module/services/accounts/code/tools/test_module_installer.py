#!/usr/bin/env python3
"""Qualify Accounts installation against a disposable loopback PostgreSQL.

Requires a local postgres:16-alpine image, Docker and Go. No cluster access,
cloud calls, provider dispatch or retained credentials. Optional
MODULE_INSTALLER_CLIENT_SCRIPT runs a separately reviewed HTTP client against
an ephemeral TLS server and the real database inside the Go integration test.
"""
import os
from pathlib import Path
import subprocess
import time
import uuid

service = Path(__file__).resolve().parents[1]
migrations = service.parents[1] / "store" / "migrations"
name = "accounts-installer-test-" + uuid.uuid4().hex[:12]

def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)

try:
    run("docker", "run", "--pull=never", "-d", "--name", name,
        "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "-e", "POSTGRES_DB=installer_test_repeatable",
        "-p", "127.0.0.1::5432", "postgres:16-alpine", stdout=subprocess.DEVNULL)
    for attempt in range(60):
        ready = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], capture_output=True)
        if ready.returncode == 0:
            break
        time.sleep(0.25)
    else:
        raise RuntimeError("disposable PostgreSQL did not become ready")
    files = sorted(migrations.glob("*.up.sql"), key=lambda path: int(path.name.split("_")[0]))
    sql = "BEGIN;\n" + "\n".join(path.read_text() for path in files) + "\nCOMMIT;\n"
    run("docker", "exec", "-i", name, "psql", "-h", "127.0.0.1", "-U", "postgres", "-d", "installer_test_repeatable",
        "-v", "ON_ERROR_STOP=1", "-q", input=sql, text=True, stdout=subprocess.DEVNULL)
    port = subprocess.check_output(["docker", "port", name, "5432"], text=True).strip().split(":")[-1]
    env = dict(os.environ, ACCOUNTS_INSTALLER_TEST_DATABASE_URL=f"postgres://postgres@127.0.0.1:{port}/installer_test_repeatable?sslmode=disable")
    run("go", "test", "-race", "./pkg/infra", "-run", "^TestModuleInstallationPostgres", "-count=1", "-v", cwd=service, env=env)
finally:
    subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
