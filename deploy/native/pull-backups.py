#!/usr/bin/env python3
"""Pull encrypted backups over SSH and verify ciphertext checksums locally."""
import argparse
import hashlib
import os
from pathlib import Path
import shlex
import subprocess


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', required=True, help='SSH user@host')
    parser.add_argument('--port', type=int, default=22)
    parser.add_argument('--identity', required=True, type=Path)
    parser.add_argument('--remote-dir', default='/var/backups/sub2api/')
    parser.add_argument('--destination', required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    args.destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    ssh = shlex.join(['ssh', '-i', str(args.identity), '-p', str(args.port),
                      '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes'])
    subprocess.run(['rsync', '-a', '--include=*.tar.gz.gpg', '--include=*.tar.gz.gpg.sha256',
                    '--exclude=*', '-e', ssh, args.host + ':' + args.remote_dir,
                    str(args.destination) + '/'], check=True)
    count = 0
    for archive in args.destination.glob('sub2api-*.tar.gz.gpg'):
        expected = archive.with_name(archive.name + '.sha256').read_text().split()[0]
        h = hashlib.sha256()
        with archive.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                h.update(chunk)
        if h.hexdigest() != expected:
            raise RuntimeError('checksum mismatch: ' + archive.name)
        count += 1
    print(f'Verified {count} encrypted backups')
