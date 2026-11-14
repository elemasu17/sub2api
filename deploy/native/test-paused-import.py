#!/usr/bin/env python3
"""Exercise paused imports against a disposable native PostgreSQL cluster."""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pg-bin', required=True, type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='sub2api-paused-') as name:
        directory = Path(name)
        data, socket = directory / 'data', directory / 'socket'
        socket.mkdir()
        # Restrict trusted connections to this private Unix socket; no TCP listener.
        subprocess.run([args.pg_bin / 'initdb', '-D', data, '-A', 'trust', '--locale=C', '-E', 'UTF8'],
                       check=True, stdout=subprocess.DEVNULL)
        with (directory / 'postgres.log').open('wb') as log:
            process = subprocess.Popen([args.pg_bin / 'postgres', '-D', data, '-h', '',
                                        '-k', socket, '-p', '55484', '-c', 'unix_socket_permissions=0700'],
                                       stdout=log, stderr=log)
            try:
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError('disposable PostgreSQL failed to start')
                    ready = subprocess.run([args.pg_bin / 'pg_isready', '-h', socket, '-p', '55484', '-d', 'postgres'],
                                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    if ready.returncode == 0:
                        break
                    time.sleep(0.1)
                else:
                    raise RuntimeError('disposable PostgreSQL startup timed out')
                subprocess.run([args.pg_bin / 'createdb', '-h', socket, '-p', '55484', 'paused_import_test'], check=True)
                env = dict(os.environ, SCOPED_ADMIN_TEST_DSN=f'host={socket} port=55484 dbname=paused_import_test sslmode=disable')
                subprocess.run(['go', 'test', '-tags=unit', './internal/handler/admin', '-run',
                                '^TestPausedCodexImportPostgres', '-count=1', '-v'], cwd=root / 'backend', env=env, check=True)
            finally:
                subprocess.run([args.pg_bin / 'pg_ctl', '-D', data, '-m', 'immediate', '-w', 'stop'],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if process.poll() is None:
                    process.kill()
                process.wait()


if __name__ == '__main__':
    main()
