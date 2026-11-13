#!/usr/bin/env python3
"""Restore a verified, decrypted backup into a NEW isolated database and directory."""
import argparse
import importlib.util
import os
from pathlib import Path
import re
import subprocess
import tarfile


def run(args, *, stdin=None):
    result = subprocess.run(args, stdin=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(Path(args[0]).name + ' failed; restore target retained for inspection')
    return result.stdout


def restore(args):
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', args.database):
        raise ValueError('use a simple, new database name')
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', args.owner):
        raise ValueError('use an existing PostgreSQL role with a simple name')
    if args.directory.exists():
        raise ValueError('restore directory must not exist')
    spec = importlib.util.spec_from_file_location('backup_verifier', Path(__file__).with_name('verify-backup.py'))
    verifier = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verifier)
    info = verifier.verify(args.archive)
    psql = ['runuser', '-u', 'postgres', '--', args.pg_bin + '/psql', '-X', '-At', '-v', 'ON_ERROR_STOP=1', '-d', 'postgres']
    exists = run(psql + ['-c', "SELECT 1 FROM pg_database WHERE datname='" + args.database + "'"]).strip()
    if exists:
        raise ValueError('restore database already exists; refusing to overwrite it')
    args.directory.mkdir(mode=0o700, parents=True)
    with tarfile.open(args.archive, 'r:gz') as archive:
        archive.extractall(args.directory, filter='data')
    run(psql + ['-c', 'CREATE DATABASE "' + args.database + '" OWNER "' + args.owner + '" TEMPLATE template0'])
    with (args.directory / 'postgres.dump').open('rb') as stream:
        run(['runuser', '-u', 'postgres', '--', args.pg_bin + '/pg_restore', '--exit-on-error',
             '--no-owner', '--no-acl', '--role=' + args.owner, '-d', args.database], stdin=stream)
    print('Verified and restored PostgreSQL into ' + args.database)
    print('Redis RDB, application and configuration extracted into ' + str(args.directory))
    print('No service was started or switched. Use isolated Redis and block outbound network for rehearsal.')
    print('Quiesced snapshot: ' + str(info['quiesced']))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--database', required=True)
    parser.add_argument('--owner', required=True)
    parser.add_argument('--directory', required=True, type=Path)
    parser.add_argument('--pg-bin', default='/usr/lib/postgresql/18/bin')
    args = parser.parse_args()
    os.umask(0o077)
    restore(args)
