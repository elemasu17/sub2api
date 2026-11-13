import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


backup = load('native_backup', 'backup.py')
verify = load('verify_backup', 'verify-backup.py')


class BackupTests(unittest.TestCase):
    def test_dump_failure_restarts_application_and_removes_plaintext_stage(self):
        calls = []

        def command(args, **kwargs):
            calls.append(args)
            if 'inspect' in args:
                return b'true\n'
            if 'pg_dump' in ' '.join(args):
                raise RuntimeError('injected dump failure')
            return b''

        with tempfile.TemporaryDirectory() as directory:
            config = {'mode': 'container', 'backup_dir': directory}
            with patch.object(backup, 'run', side_effect=command):
                with self.assertRaisesRegex(RuntimeError, 'dump failure'):
                    backup.snapshot(config, quiesce=True, label='test')
            self.assertIn(['docker', 'start', 'sub2api'], calls)
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_unsafe_archive_is_rejected_before_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            archive_path = Path(directory) / 'bad.tar.gz'
            with tarfile.open(archive_path, 'w:gz') as archive:
                entry = tarfile.TarInfo('../../outside')
                entry.size = 1
                archive.addfile(entry, io.BytesIO(b'x'))
            with self.assertRaisesRegex(ValueError, 'unsafe'):
                verify.verify(archive_path)

    def test_tampered_database_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            archive_path = Path(directory) / 'bad.tar.gz'
            manifest = {'format': 1, 'files': {'postgres.dump': '0' * 64}}
            with tarfile.open(archive_path, 'w:gz') as archive:
                for name, content in [('manifest.json', json.dumps(manifest).encode()),
                                      ('postgres.dump', b'changed')]:
                    entry = tarfile.TarInfo(name)
                    entry.size = len(content)
                    archive.addfile(entry, io.BytesIO(content))
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                verify.verify(archive_path)


if __name__ == '__main__':
    unittest.main()
