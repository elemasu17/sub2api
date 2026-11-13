import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
from types import SimpleNamespace
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


class DeploymentFailureTests(unittest.TestCase):
    def run_deployment(self, *, healthy, backup_fails=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = root / 'releases' / '1.0.0'
            old.mkdir(parents=True)
            current = root / 'current'
            current.symlink_to(old)
            args = SimpleNamespace(root=str(root), archive=root / 'release.tar.gz', sha256='0' * 64,
                                   database='example', service='example.service', health_url='http://localhost')
            calls = []
            records_at_start = []

            def command(command):
                calls.append(command)
                if command[0] == '/usr/local/sbin/sub2api-backup':
                    if backup_fails:
                        raise RuntimeError('injected backup failure')
                    return b'/protected/before-release.tar.gz.gpg\n'
                if command == ['systemctl', 'start', 'example.service'] and not backup_fails:
                    records_at_start.append(json.loads((root / 'last-deployment.json').read_text()))
                return b''

            manifest = {'version': '1.0.1', 'commit': 'source-commit'}
            activate = deploy.activate

            def activate_after_record(target, link):
                saved = json.loads((root / 'last-deployment.json').read_text())
                self.assertEqual(saved['phase'], 'prepared')
                self.assertEqual(saved['backup'], '/protected/before-release.tar.gz.gpg')
                self.assertEqual(link.resolve(), old.resolve())
                activate(target, link)

            with patch.object(deploy, 'verify_release', return_value=manifest), \
                 patch.object(deploy, 'activate', side_effect=activate_after_record), \
                 patch.object(deploy, 'run', side_effect=command), \
                 patch.object(deploy.subprocess, 'run', return_value=SimpleNamespace(returncode=0)), \
                 patch.object(deploy, 'migration_signature', return_value='unchanged'), \
                 patch.object(deploy, 'health', return_value=healthy):
                if healthy and not backup_fails:
                    deploy.deploy(args)
                else:
                    with self.assertRaisesRegex(RuntimeError, 'backup failure' if backup_fails else 'without automatic rollback'):
                        deploy.deploy(args)
            record_path = root / 'last-deployment.json'
            record = json.loads(record_path.read_text()) if record_path.exists() else None
            return calls, records_at_start, record, current.resolve().name

    def test_failed_health_with_unchanged_migrations_keeps_service_stopped(self):
        calls, starts, record, current = self.run_deployment(healthy=False)
        self.assertEqual(current, '1.0.1')
        self.assertEqual(calls.count(['systemctl', 'start', 'example.service']), 1)
        self.assertEqual(calls[-1], ['systemctl', 'stop', 'example.service'])
        self.assertEqual(record['phase'], 'failed_stopped')
        self.assertEqual(record['migration_signature_after'], 'unchanged')
        self.assertEqual(starts[0]['backup'], '/protected/before-release.tar.gz.gpg')
        self.assertEqual(starts[0]['phase'], 'activated')

    def test_success_records_backup_before_start_and_completed_after_health(self):
        calls, starts, record, current = self.run_deployment(healthy=True)
        self.assertEqual(current, '1.0.1')
        self.assertEqual(record['phase'], 'completed')
        self.assertTrue(record['previous_release'].endswith('/1.0.0'))
        self.assertEqual(starts[0]['phase'], 'activated')
        self.assertEqual(starts[0]['backup'], record['backup'])
        self.assertEqual(calls[-1], ['systemctl', 'start', 'example.service'])

    def test_backup_failure_preserves_and_restarts_old_program(self):
        calls, starts, record, current = self.run_deployment(healthy=False, backup_fails=True)
        self.assertEqual(current, '1.0.0')
        self.assertEqual(calls[-1], ['systemctl', 'start', 'example.service'])
        self.assertIsNone(record)
        self.assertEqual(starts, [])

    def test_recovery_record_is_visible_before_activation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            record = {'phase': 'prepared', 'backup': '/protected/recovery.tar.gz.gpg'}
            deploy.record_deployment(root, record)
            self.assertEqual(json.loads((root / 'last-deployment.json').read_text()), record)
            self.assertEqual(list(root.glob('.deployment-*')), [])
            self.assertEqual((root / 'last-deployment.json').stat().st_mode & 0o777, 0o600)


if __name__ == '__main__':
    unittest.main()
