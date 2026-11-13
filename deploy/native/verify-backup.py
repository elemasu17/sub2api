#!/usr/bin/env python3
"""Verify a decrypted backup without restoring any production state."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import tarfile


def verify(path):
    with tarfile.open(path, 'r:gz') as archive:
        members = archive.getmembers()
        names = set()
        for member in members:
            normalized = PurePosixPath(member.name)
            if normalized.is_absolute() or '..' in normalized.parts or member.name in names:
                raise ValueError('unsafe or duplicate archive entry')
            if not (member.isfile() or member.isdir()):
                raise ValueError('only regular files and directories are permitted')
            names.add(member.name)
        manifest = json.load(archive.extractfile('manifest.json'))
        if manifest.get('format') != 1:
            raise ValueError('unknown backup format')
        expected = set(manifest['files']) | {'manifest.json'}
        actual = {m.name for m in members if m.isfile()}
        if expected != actual:
            raise ValueError('manifest file set mismatch')
        for name, expected_hash in manifest['files'].items():
            h = hashlib.sha256()
            with archive.extractfile(name) as stream:
                for block in iter(lambda: stream.read(1024 * 1024), b''):
                    h.update(block)
            if h.hexdigest() != expected_hash:
                raise ValueError('checksum mismatch: ' + name)
        for required in ['postgres.dump', 'postgres-globals.sql', 'redis.rdb', 'application/sub2api']:
            if required not in actual:
                raise ValueError('missing recovery component: ' + required)
    return {'verified_files': len(manifest['files']), 'created_at': manifest['created_at'],
            'mode': manifest['mode'], 'quiesced': manifest['quiesced']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    args = parser.parse_args()
    print(json.dumps(verify(args.archive)))
