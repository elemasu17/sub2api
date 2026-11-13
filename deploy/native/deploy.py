#!/usr/bin/env python3
"""Install a verified native release; retain data and refuse unsafe automatic rollback."""
import argparse
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
import time
import urllib.request


def run(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(Path(args[0]).name + ' failed; inspect its service log locally')
    return result.stdout


def verify_release(archive, expected_sha256, destination):
    if not re.fullmatch('[a-f0-9]{64}', expected_sha256):
        raise ValueError('SHA256 must be supplied from the trusted release manifest')
    if hashlib.sha256(archive.read_bytes()).hexdigest() != expected_sha256:
        raise ValueError('release checksum mismatch')
    with tarfile.open(archive, 'r:gz') as tar:
        for member in tar.getmembers():
            path = Path(member.name)
            if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
                raise ValueError('unsafe release member')
        tar.extractall(destination, filter='data')
    manifest = json.loads((destination / 'manifest.json').read_text())
    if manifest.get('dirty_source') or manifest.get('os') != 'linux' or manifest.get('build_type') != 'source':
        raise ValueError('only clean, native source releases may be deployed')
    actual = {str(p.relative_to(destination)) for p in destination.rglob('*') if p.is_file()}
    if actual != set(manifest['files']) | {'manifest.json'}:
        raise ValueError('unexpected release files')
    for name, digest in manifest['files'].items():
        if hashlib.sha256((destination / name).read_bytes()).hexdigest() != digest:
            raise ValueError('invalid release file: ' + name)
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?', manifest['version']):
        raise ValueError('invalid version')
    host_arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(os.uname().machine)
    if manifest['arch'] != host_arch:
        raise ValueError('release does not match host architecture')
    return manifest


def migration_signature(database):
    data = run(['runuser', '-u', 'postgres', '--', 'psql', '-X', '-At', '-d', database,
                '-c', 'SELECT filename, checksum FROM schema_migrations ORDER BY filename'])
    return hashlib.sha256(data).hexdigest()


def activate(target, current):
    pending = current.with_name('current.new')
    pending.unlink(missing_ok=True)
    pending.symlink_to(target)
    os.replace(pending, current)


def health(url, version):
    for _ in range(45):
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                data = json.load(response)
                if data.get('data', data).get('version') == version:
                    return True
        except Exception:
            pass
        time.sleep(1)
    return False


def deploy(args):
    root = Path(args.root)
    releases = root / 'releases'
    releases.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.release-', dir=releases))
    current = root / 'current'
    previous = current.resolve() if current.is_symlink() else None
    try:
        manifest = verify_release(args.archive, args.sha256, stage)
        target = releases / manifest['version']
        if target.exists():
            raise ValueError('release version already installed; use a new version')
        for path in stage.rglob('*'):
            path.chmod(0o755 if path.is_dir() or path.name == 'sub2api' else 0o644)
        stage.chmod(0o755)
        os.replace(stage, target)
        before = migration_signature(args.database)
        active = subprocess.run(['systemctl', 'is-active', '--quiet', args.service]).returncode == 0
        run(['systemctl', 'stop', args.service])
        try:
            # Service is already stopped, so backup keeps it stopped until activation.
            backup = run(['/usr/local/sbin/sub2api-backup', '--quiesce', '--label', 'before-release']).decode().strip()
        except BaseException:
            if active:
                run(['systemctl', 'start', args.service])
            raise
        activate(target, current)
        try:
            run(['systemctl', 'start', args.service])
            if not health(args.health_url, manifest['version']):
                raise RuntimeError('release health check failed')
        except BaseException:
            run(['systemctl', 'stop', args.service])
            after = migration_signature(args.database)
            if previous and before == after:
                activate(previous, current)
                run(['systemctl', 'start', args.service])
                raise RuntimeError('new release failed; previous program restored with unchanged migrations')
            raise RuntimeError('new release failed after database changes; service stopped, restore the recorded backup')
        record = {'version': manifest['version'], 'commit': manifest['commit'],
                  'previous_release': str(previous) if previous else None,
                  'backup': backup, 'migration_signature_before': before,
                  'migration_signature_after': migration_signature(args.database)}
        (root / 'last-deployment.json').write_text(json.dumps(record, indent=2) + '\n')
        print(json.dumps(record))
    finally:
        if stage.exists():
            shutil.rmtree(stage)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--sha256', required=True)
    parser.add_argument('--database', required=True)
    parser.add_argument('--root', default='/opt/sub2api')
    parser.add_argument('--service', default='sub2api.service')
    parser.add_argument('--health-url', default='http://127.0.0.1:8080/api/v1/settings/public')
    args = parser.parse_args()
    os.umask(0o077)
    with open('/run/lock/sub2api-deploy.lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        deploy(args)
