import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('native_deploy', Path(__file__).resolve().parents[1] / 'deploy.py')
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class ReleaseTests(unittest.TestCase):
    def test_wrong_checksum_never_extracts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / 'release.tar.gz'
            archive.write_bytes(b'changed archive')
            target = root / 'target'
            target.mkdir()
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                deploy.verify_release(archive, '0' * 64, target)
            self.assertEqual(list(target.iterdir()), [])

    def test_dirty_build_cannot_be_published(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / 'release.tar.gz'
            payload = json.dumps({'dirty_source': True, 'os': 'linux', 'build_type': 'source'}).encode()
            with tarfile.open(archive, 'w:gz') as stream:
                info = tarfile.TarInfo('manifest.json')
                info.size = len(payload)
                stream.addfile(info, io.BytesIO(payload))
            target = root / 'target'
            target.mkdir()
            with self.assertRaisesRegex(ValueError, 'clean'):
                deploy.verify_release(archive, hashlib.sha256(archive.read_bytes()).hexdigest(), target)

    def test_activation_replaces_only_current_link(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old, new = root / 'old', root / 'new'
            old.mkdir(); new.mkdir()
            current = root / 'current'
            current.symlink_to(old)
            deploy.activate(new, current)
            self.assertEqual(current.resolve(), new.resolve())
            self.assertTrue(old.is_dir())


if __name__ == '__main__':
    unittest.main()
