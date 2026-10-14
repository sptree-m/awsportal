"""The mandatory hook must not leak its caller's repository into Git tests."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class GitHookIsolationTest(unittest.TestCase):
    def test_hook_clears_repository_environment_before_integration_tests(self):
        hook = Path(__file__).resolve().parents[1] / '.githooks/pre-commit'
        with tempfile.TemporaryDirectory() as td:
            root = Path(td) / 'parent'
            root.mkdir()
            def git(*args, env=None):
                return subprocess.run(['git', *args], cwd=root, env=env,
                                      check=True, capture_output=True, text=True)
            git('init', '-q')
            git('config', 'user.name', 'Hook Test')
            git('config', 'user.email', 'hook@example.invalid')
            git('commit', '--allow-empty', '-qm', 'initial')
            (root / '.githooks').mkdir()
            shutil.copyfile(hook, root / '.githooks/pre-commit')
            (root / '.githooks/pre-commit').chmod(0o755)
            (root / 'scripts').mkdir()
            stub = root / 'scripts/test-all.sh'
            stub.write_text('''#!/usr/bin/env bash
set -euo pipefail
test -z "${GIT_DIR:-}"
test -z "${GIT_WORK_TREE:-}"
test -z "${GIT_INDEX_FILE:-}"
git init --bare -q "$TEST_CHILD_REPOSITORY"
''')
            stub.chmod(0o755)
            git('config', 'core.hooksPath', '.githooks')
            (root / 'parent.txt').write_text('protected repository\n')
            git('add', 'parent.txt')
            env = dict(os.environ, GIT_DIR=str(root / '.git'),
                       GIT_WORK_TREE=str(root), GIT_INDEX_FILE=str(root / '.git/index'),
                       TEST_CHILD_REPOSITORY=str(Path(td) / 'child.git'))
            git('commit', '-qm', 'parent changed', env=env)
            self.assertEqual(git('log', '-1', '--format=%s').stdout.strip(), 'parent changed')
            self.assertEqual(git('config', '--get', 'core.bare').stdout.strip(), 'false')
            self.assertEqual(git('ls-files').stdout.strip(), 'parent.txt')
            self.assertTrue((Path(td) / 'child.git/HEAD').exists())


if __name__ == '__main__':
    unittest.main()
