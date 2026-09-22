#!/usr/bin/env python3
"""Build the pinned migration CLI and run the disposable PostgreSQL proof."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
ENGINE = 'github.com/codefly-dev/service-postgres@v0.0.133-0.20260911141250-af749da8e81c'
SUM = 'h1:vEvMvg7JW452jlIVPLjRuqjLgqK+TkRXeeFnc+RDI6s='


def main():
    env = {**os.environ, 'GOWORK': 'off', 'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': 'amd64'}
    module = json.loads(subprocess.check_output(['go', 'mod', 'download', '-json', ENGINE], cwd=ROOT, env=env, text=True))
    if module.get('Sum') != SUM:
        raise RuntimeError('published migration primitive checksum mismatch')
    with tempfile.TemporaryDirectory(prefix='managed-migrate-') as tmp:
        binary = Path(tmp) / 'migrate'
        subprocess.run(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-buildid=',
                        '-tags', 'postgres', '-o', str(binary), 'github.com/golang-migrate/migrate/v4/cmd/migrate'],
                       cwd=module['Dir'], env=env, check=True)
        subprocess.run([sys.executable, str(ROOT / 'qualification/managed-database/run.py'),
                        '--migrate', str(binary), '--output', sys.argv[1]], cwd=ROOT, check=True)


if __name__ == '__main__':
    main()
