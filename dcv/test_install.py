"""Check the installer's actual validation and rendered DCV configuration safely."""
import configparser
import os
from pathlib import Path
import re
import subprocess
import unittest


class InstallPortTest(unittest.TestCase):
    def setUp(self):
        source = Path(__file__).with_name("install.sh").read_text()
        # Stop before root checks/package installation; never execute installer mutations.
        self.validation = source.split('[[ "$(id -u)"', 1)[0]
        self.template = re.search(r"cat > /etc/dcv/dcv.conf <<CONF\n(.*?)\nCONF", source, re.S).group(1)

    def render(self, value):
        env = dict(os.environ, AWSPORTAL_DCV_PORT=value)
        return subprocess.run(["bash"], input=self.validation + "\ncat <<CONF\n" + self.template + "\nCONF\n",
                              env=env, text=True, capture_output=True)

    def test_default_and_custom_port_preserve_authentication_and_browser_block(self):
        for value in ("", "443", "8443", "10443", "1", "65535"):
            with self.subTest(value=value):
                result = self.render(value)
                self.assertEqual(result.returncode, 0, result.stderr)
                config = configparser.ConfigParser()
                config.read_string(result.stdout)
                self.assertEqual(config["connectivity"]["web-port"], value or "8443")
                self.assertEqual(config["connectivity"]["enable-quic-frontend"], "false")
                self.assertEqual(config["security"]["auth-token-verifier"], '"http://127.0.0.1:8444"')
                self.assertEqual(config["security"]["allowed-ws-origin-regex"], '"^$"')

    def test_invalid_or_reserved_port_fails_before_installation(self):
        for value in ("0", "65536", "-1", "443.5", "+443", "0443", " 443", "443/", "abc", "22", "3389", "8444"):
            with self.subTest(value=value):
                result = self.render(value)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("AWSPORTAL_DCV_PORT must be", result.stderr)
                self.assertEqual(result.stdout, "")
