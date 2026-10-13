import importlib.util
from pathlib import Path
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'cmd/awsportal/preparation/setup-unix.py'
spec = importlib.util.spec_from_file_location('client_preparation', SCRIPT)
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)


class HostsUpdateTest(unittest.TestCase):
    def test_preserves_unrelated_aliases_comments_and_repeats(self):
        text = '# company hosts\n127.0.0.1 localhost\n192.0.2.20 portal.company.example unrelated.example # keep note\n192.0.2.30 portal.company.example\n'
        mappings = [{'name': 'portal.company.example', 'ip': '192.0.2.10'}]
        revised = client.updated_hosts(text, mappings)
        self.assertIn('127.0.0.1 localhost', revised)
        self.assertIn('192.0.2.20\tunrelated.example # keep note', revised)
        self.assertEqual(revised.count('portal.company.example'), 1)
        self.assertIn('192.0.2.10\tportal.company.example # awsportal', revised)
        self.assertEqual(client.updated_hosts(revised, mappings), revised)

    def test_multiple_hosts_ipv6_and_case(self):
        mappings = [{'name': 'portal.company.example', 'ip': '::1'}, {'name': 'proxy.company.example', 'ip': '192.0.2.11'}]
        revised = client.updated_hosts('192.0.2.90 PORTAL.company.example alias.example\n', mappings)
        self.assertIn('192.0.2.90\talias.example', revised)
        self.assertIn('::1\tportal.company.example', revised)
        self.assertIn('192.0.2.11\tproxy.company.example', revised)


if __name__ == '__main__':
    unittest.main()
