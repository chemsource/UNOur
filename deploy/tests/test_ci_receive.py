"""Protocol, integrity and durable environment behavior of the SSH receiver."""

import hashlib
import importlib.util
import io
from pathlib import Path
import stat
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('ci_receive', Path(__file__).parents[1] / 'ci_receive.py')
receiver = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = receiver
spec.loader.exec_module(receiver)


class ProtocolTests(unittest.TestCase):
    def test_accepts_only_a_digest_and_numeric_run_identity(self):
        release = receiver.parse_command('deploy ' + 'a' * 40 + ' 42 2 ' + 'b' * 64)
        self.assertEqual((release.sha, release.run, release.attempt, release.digest), ('a' * 40, 42, 2, 'b' * 64))

    def test_rejects_shell_commands_paths_and_invalid_run_identity(self):
        for command in ('sh', 'deploy ../../etc 1 1 ' + 'b' * 64,
                        'deploy ' + 'a' * 40 + ' 0 1 ' + 'b' * 64,
                        'deploy ' + 'a' * 40 + ' 1 -1 ' + 'b' * 64,
                        'deploy ' + 'a' * 40 + ' 1 1 ' + 'b' * 64 + '; id'):
            with self.subTest(command=command), self.assertRaises(ValueError):
                receiver.parse_command(command)

    def test_older_or_duplicate_runs_cannot_replace_a_newer_success(self):
        previous = {'run': 42, 'attempt': 2, 'sha': 'a' * 40}
        self.assertTrue(receiver.superseded(receiver.Release('c' * 40, 41, 9, 'b' * 64), previous))
        self.assertTrue(receiver.superseded(receiver.Release('a' * 40, 42, 2, 'b' * 64), previous))
        self.assertFalse(receiver.superseded(receiver.Release('a' * 40, 42, 3, 'b' * 64), previous))
        self.assertFalse(receiver.superseded(receiver.Release('c' * 40, 43, 1, 'b' * 64), previous))
        with self.assertRaises(ValueError):
            receiver.superseded(receiver.Release('c' * 40, 42, 3, 'b' * 64), previous)


class DurableFilesTests(unittest.TestCase):
    def test_component_update_preserves_the_other_image_and_environment(self):
        before = '# production\nUNOUR_API_RELEASE=old-api\nUNOUR_WEB_RELEASE=old-web\nUNO_ORIGINS=https://csac.chat\nUNOUR_API_RELEASE=duplicate\n'
        self.assertEqual(receiver.with_releases(before, {'api': 'new-api'}),
                         '# production\nUNOUR_API_RELEASE=new-api\nUNOUR_WEB_RELEASE=old-web\nUNO_ORIGINS=https://csac.chat\n')

    def test_writes_environment_privately_and_replaces_complete_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / '.env'
            receiver.atomic_write(path, 'original\n')
            receiver.atomic_write(path, 'complete replacement\n')
            self.assertEqual(path.read_text(), 'complete replacement\n')
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_integrity_failure_cannot_leave_an_accepted_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.tar.gz'
            with self.assertRaises(ValueError):
                receiver.receive_image(io.BytesIO(b'wrong image'), path, 'a' * 64, 100)
            self.assertFalse(path.exists())

    def test_upload_limit_rejects_oversized_input(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.tar.gz'
            with self.assertRaises(ValueError):
                receiver.receive_image(io.BytesIO(b'123456'), path, hashlib.sha256(b'123456').hexdigest(), 5)
            self.assertFalse(path.exists())

    def test_verified_upload_matches_its_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.tar.gz'
            receiver.receive_image(io.BytesIO(b'actual bytes'), path, hashlib.sha256(b'actual bytes').hexdigest(), 100)
            self.assertEqual(path.read_bytes(), b'actual bytes')


if __name__ == '__main__':
    unittest.main()
