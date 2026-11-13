#!/usr/bin/env python3
"""Encrypted local backups. Configuration is root-only JSON, never shell sourced."""
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile


def run(args, *, output=None, env=None):
    result = subprocess.run(args, stdout=output or subprocess.PIPE,
                            stderr=subprocess.PIPE, env=env, check=False)
    if result.returncode:
        # Commands may process secrets. Never log their output or full argv.
        raise RuntimeError(f'{Path(args[0]).name} failed (exit {result.returncode})')
    return result.stdout or b''


def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def encrypt_tree(stage, target, recipient):
    pending = target.with_name(target.name + '.partial')
    try:
        with pending.open('xb') as encrypted:
            process = subprocess.Popen(
                ['gpg', '--batch', '--yes', '--trust-model', 'always',
                 '--recipient-file', str(recipient), '--encrypt'],
                stdin=subprocess.PIPE, stdout=encrypted, stderr=subprocess.PIPE)
            try:
                with tarfile.open(fileobj=process.stdin, mode='w|gz') as archive:
                    for child in sorted(stage.iterdir()):
                        archive.add(child, arcname=child.name, recursive=True)
                process.stdin.close()
                stderr = process.stderr.read()
                status = process.wait()
                if status:
                    raise RuntimeError(f'backup encryption failed (exit {status})')
            except BaseException:
                process.kill()
                process.wait()
                raise
            encrypted.flush()
            os.fsync(encrypted.fileno())
        os.replace(pending, target)
    finally:
        pending.unlink(missing_ok=True)


def snapshot(config, *, quiesce, label):
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,48}', label):
        raise ValueError('invalid backup label')
    mode = config['mode']
    if mode not in ('container', 'native'):
        raise ValueError('mode must be container or native')
    backup_dir = Path(config['backup_dir'])
    backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    stamp = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    destination = backup_dir / f'sub2api-{stamp}-{label}.tar.gz.gpg'
    stage = Path(tempfile.mkdtemp(prefix='.stage-', dir=backup_dir))
    restart = False
    redis_temp = '/tmp/sub2api-backup-' + stamp + '.rdb'
    try:
        if mode == 'container':
            app = config.get('app_container', 'sub2api')
            pg = config.get('postgres_container', 'sub2api-postgres')
            redis = config.get('redis_container', 'sub2api-redis')
            active = run(['docker', 'inspect', '-f', '{{.State.Running}}', app]).strip() == b'true'
            if quiesce and active:
                run(['docker', 'stop', '--time', '180', app])
                restart = True
            with (stage / 'postgres.dump').open('wb') as output:
                run(['docker', 'exec', pg, 'sh', '-c',
                     'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc --no-owner --no-acl'], output=output)
            with (stage / 'postgres-globals.sql').open('wb') as output:
                run(['docker', 'exec', pg, 'sh', '-c',
                     'exec pg_dumpall -U "$POSTGRES_USER" --globals-only'], output=output)
            # A replication RDB is self-contained; never copy a changing AOF directory.
            redis_env = dict(os.environ, REDISCLI_AUTH='')
            password_file = config.get('redis_password_file')
            if password_file:
                redis_env['REDISCLI_AUTH'] = Path(password_file).read_text().strip()
            if password_file:
                command = ['docker', 'exec', '-e', 'REDISCLI_AUTH', redis, 'redis-cli']
            else:
                command = ['docker', 'exec', redis, 'env', '-u', 'REDISCLI_AUTH', 'redis-cli']
            run(command + ['--rdb', redis_temp], env=redis_env)
            run(['docker', 'cp', redis + ':' + redis_temp, str(stage / 'redis.rdb')])
            run(['docker', 'exec', redis, 'rm', '-f', redis_temp])
            release = stage / 'application'
            release.mkdir()
            # Preserve the running binary, which may differ from the original image.
            run(['docker', 'cp', app + ':/app/sub2api', str(release / 'sub2api')])
            run(['docker', 'cp', app + ':/app/resources', str(release / 'resources')])
            pg_version = run(['docker', 'exec', pg, 'pg_dump', '--version']).decode().strip()
        else:
            app = config.get('app_service', 'sub2api.service')
            active = subprocess.run(['systemctl', 'is-active', '--quiet', app]).returncode == 0
            if quiesce and active:
                run(['systemctl', 'stop', app])
                restart = True
            pg_bin = config.get('pg_bin', '/usr/lib/postgresql/18/bin')
            database = config['database']
            with (stage / 'postgres.dump').open('wb') as output:
                run(['runuser', '-u', 'postgres', '--', pg_bin + '/pg_dump', '-d', database,
                     '-Fc', '--no-owner', '--no-acl'], output=output)
            with (stage / 'postgres-globals.sql').open('wb') as output:
                run(['runuser', '-u', 'postgres', '--', pg_bin + '/pg_dumpall', '--globals-only'], output=output)
            redis_env = dict(os.environ, REDISCLI_AUTH='')
            password_file = config.get('redis_password_file')
            if password_file:
                redis_env['REDISCLI_AUTH'] = Path(password_file).read_text().strip()
            else:
                redis_env.pop('REDISCLI_AUTH', None)
            redis_command = [config.get('redis_cli', '/usr/bin/redis-cli'),
                             '-h', config.get('redis_host', '127.0.0.1'),
                             '-p', str(config.get('redis_port', 6379))]
            run(redis_command + ['--rdb', str(stage / 'redis.rdb')], env=redis_env)
            shutil.copytree(Path(config['release_dir']).resolve(), stage / 'application')
            pg_version = run([pg_bin + '/pg_dump', '--version']).decode().strip()
        for name, source in config['paths'].items():
            if not re.fullmatch(r'[a-zA-Z0-9_-]+', name):
                raise ValueError('invalid archive path label')
            path = Path(source)
            if path.is_dir():
                shutil.copytree(path, stage / name, symlinks=False,
                                ignore=shutil.ignore_patterns('*.sock'))
            elif path.is_file():
                shutil.copy2(path, stage / name)
            else:
                raise RuntimeError('configured backup path is missing: ' + name)
        if restart:
            run(['docker', 'start', app] if mode == 'container' else ['systemctl', 'start', app])
            restart = False
        files = {str(path.relative_to(stage)): digest(path)
                 for path in stage.rglob('*') if path.is_file() and not path.is_symlink()}
        manifest = {'format': 1, 'created_at': stamp, 'mode': mode, 'quiesced': quiesce,
                    'postgres_version': pg_version, 'files': files,
                    'redis_format': 'standalone RDB; restore with old AOF removed from target',
                    'consistency': 'application stopped' if quiesce else 'independent online database and Redis snapshots'}
        (stage / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        encrypt_tree(stage, destination, Path(config['recipient_file']))
        checksum = destination.with_name(destination.name + '.sha256')
        checksum.write_text(digest(destination) + '  ' + destination.name + '\n')
        return destination
    finally:
        try:
            if restart:
                run(['docker', 'start', app] if mode == 'container' else ['systemctl', 'start', app])
        finally:
            shutil.rmtree(stage)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', default='/etc/sub2api/backup/config.json')
    parser.add_argument('--quiesce', action='store_true', help='stop application while snapshotting')
    parser.add_argument('--label', default='daily')
    args = parser.parse_args()
    os.umask(0o077)
    config = json.loads(Path(args.config).read_text())
    with open(config.get('lock_file', '/run/lock/sub2api-backup.lock'), 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        print(snapshot(config, quiesce=args.quiesce, label=args.label))


if __name__ == '__main__':
    main()
