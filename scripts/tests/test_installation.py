"""Local script regressions. Fake Incus records commands; no host changes occur."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class InstallationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.env = dict(os.environ, PATH=str(self.work) + os.pathsep + os.environ['PATH'])

    def tool(self, name, body):
        path = self.work / name
        path.write_text('#!/usr/bin/env python3\n' + body)
        path.chmod(0o755)

    def installer_function(self, name):
        source = (ROOT / 'scripts/install.sh').read_text()
        return re.search(r'^' + name + r'\(\) \{\n.*?^\}', source, re.M | re.S)[0]

    def test_go_version_covers_both_modules(self):
        # Numeric ordering must handle 1.9 versus 1.27, independently of which
        # module currently has the newer minimum.
        (self.work / 'agent/shelley').mkdir(parents=True)
        for gateway, agent, want in [('1.9.9', '1.27.1', '1.27.1'),
                                     ('1.28.0', '1.27.1', '1.28.0')]:
            (self.work / 'go.mod').write_text('module gateway\ngo ' + gateway + '\n')
            (self.work / 'agent/shelley/go.mod').write_text('module agent\ngo ' + agent + '\n')
            result = subprocess.run(['bash', '-euc', self.installer_function('detect_go_version') +
                                     '\ndetect_go_version'], env=dict(self.env, REPO_ROOT=str(self.work)),
                                    text=True, capture_output=True, check=True)
            self.assertEqual(result.stdout.strip(), want)

    def test_failed_go_download_preserves_existing_toolchain(self):
        install_dir = self.work / 'existing-go'
        install_dir.mkdir()
        marker = install_dir / 'keep'
        marker.write_text('working toolchain')
        self.tool('curl', 'raise SystemExit(22)\n')
        source = 'log() { :; }; die() { echo "$*" >&2; exit 1; };\n'
        source += self.installer_function('install_go') + '\ninstall_go 1.27.1'
        result = subprocess.run(['bash', '-euc', source],
                                env=dict(self.env, GO_INSTALL_DIR=str(install_dir), ARCH='arm64'),
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Failed to download required Go', result.stderr)
        self.assertEqual(marker.read_text(), 'working toolchain')

    def test_invalid_go_archive_preserves_existing_toolchain(self):
        install_dir = self.work / 'existing-go'
        install_dir.mkdir()
        marker = install_dir / 'keep'
        marker.write_text('working toolchain')
        self.tool('curl', "import pathlib, sys\npathlib.Path(sys.argv[sys.argv.index('-o') + 1]).write_text('invalid archive')\n")
        source = 'log() { :; }; die() { echo "$*" >&2; exit 1; };\n'
        source += self.installer_function('install_go') + '\ninstall_go 1.27.1'
        result = subprocess.run(['bash', '-euc', source],
                                env=dict(self.env, GO_INSTALL_DIR=str(install_dir), ARCH='arm64'),
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Invalid Go archive', result.stderr)
        self.assertEqual(marker.read_text(), 'working toolchain')

    def image_run(self, fail=False):
        calls = self.work / 'calls.jsonl'
        artifact = self.work / 'picoclaw'
        artifact.touch()
        self.tool('sleep', 'pass\n')
        self.tool('incus', '''import json, os, sys
args = sys.argv[1:]
with open(os.environ['CALLS'], 'a') as f:
    f.write(json.dumps(args) + '\\n')
if args[:3] == ['image', 'alias', 'list']:
    print('svkexe-base,old-image')
if os.environ['FAIL_APT'] == '1' and args[0] == 'exec' and 'apt-get update' in args[-1]:
    raise SystemExit(1)
if args[0] == 'exec' and args[-1] == '/usr/local/bin/picoclaw version':
    print('{"customized": true}')
''')
        result = subprocess.run(['bash', str(ROOT / 'scripts/build-image.sh')],
                                env=dict(self.env, CALLS=str(calls), FAIL_APT=str(int(fail)),
                                         SVKEXE_AGENT_BINARY=str(artifact), SKIP_CLAUDE='1', SKIP_CODEX='1'),
                                text=True, capture_output=True)
        return result, [json.loads(line) for line in calls.read_text().splitlines()]

    def test_failed_image_build_keeps_old_image(self):
        result, calls = self.image_run(fail=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c[:2] == ['image', 'delete'] or c[0] == 'publish' for c in calls))
        self.assertTrue(any(c[:2] == ['delete', '--force'] for c in calls))

    def test_successful_image_build_reuses_alias_after_validation(self):
        result, calls = self.image_run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        publish = next(i for i, c in enumerate(calls) if c[0] == 'publish')
        validated = next(i for i, c in enumerate(calls) if c[-1] == '/usr/local/bin/picoclaw version')
        self.assertLess(validated, publish)
        self.assertIn('--reuse', calls[publish])
        self.assertFalse(any(c[:2] == ['image', 'delete'] for c in calls))


if __name__ == '__main__':
    unittest.main()
