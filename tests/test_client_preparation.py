import importlib.util
from pathlib import Path
import unittest
import json
import subprocess
import tempfile
import hashlib
import ssl

SCRIPT = Path(__file__).resolve().parents[1] / 'cmd/awsportal/preparation/setup-unix.py'
GENERATOR = SCRIPT.parent.parent / 'adminsupport/generate-preparation.py'
gen_spec = importlib.util.spec_from_file_location('preparation_generator', GENERATOR)
generator = importlib.util.module_from_spec(gen_spec)
gen_spec.loader.exec_module(generator)


class PreparationGeneratorTest(unittest.TestCase):
    def test_inventory_64_128_and_rejects_invalid(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'hosts.json'
            for count in (64, 128, 129):
                hosts = [{'name': f'dcv{i}.company.example', 'ip': f'192.0.2.{i+1}'} for i in range(count)]
                path.write_text(json.dumps({'hosts': hosts}))
                if count > 128:
                    with self.assertRaises(ValueError): generator.load_hosts(path)
                else:
                    self.assertEqual(generator.load_hosts(path), hosts)
            for hosts in ([], [{'name': '../escape', 'ip': '192.0.2.1'}], [{'name': 'portal.example', 'ip': '0.0.0.0'}], [{'name': 'portal.example', 'ip': '192.0.2.1'}, {'name': 'PORTAL.example', 'ip': '192.0.2.2'}]):
                path.write_text(json.dumps({'hosts': hosts}))
                with self.assertRaises(ValueError): generator.load_hosts(path)

    def test_real_openssl_signing_and_64_client_mappings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ca, key = root / 'ca.crt', root / 'ca.key'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '730', '-keyout', str(key), '-out', str(ca), '-subj', '/CN=Test CA', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign'], check=True, capture_output=True)
            hosts = [{'name': 'portal.company.example', 'ip': '192.0.2.10'}, {'name': 'dcv.company.example', 'ip': '192.0.2.11'}]
            output = root / 'signed'
            generator.generate(hosts, output, ca, key)
            serials = set()
            for h in hosts:
                folder = output / h['name']
                subprocess.run(['openssl', 'verify', '-CAfile', str(ca), '-verify_hostname', h['name'], str(folder / 'server.crt')], check=True, capture_output=True)
                serials.add(subprocess.run(['openssl', 'x509', '-in', str(folder / 'server.crt'), '-noout', '-serial'], check=True, capture_output=True).stdout)
                self.assertEqual((folder / 'server.key').stat().st_mode & 0o777, 0o600)
            self.assertEqual(len(serials), 2)
            self.assertFalse((output / 'ca.key').exists())
            with self.assertRaises(FileExistsError): generator.generate(hosts, output)
            generator.generate(hosts[:1], root / 'csr')
            self.assertTrue((root / 'csr' / hosts[0]['name'] / 'server.csr').exists())
            self.assertFalse((root / 'csr' / hosts[0]['name'] / 'server.crt').exists())
            fingerprint = hashlib.sha256(ssl.PEM_cert_to_DER_cert(ca.read_text())).hexdigest().upper()
            config = {'hosts': [{'name': f'dcv{i}.company.example', 'ip': f'192.0.2.{i+1}'} for i in range(64)], 'ca_sha256': fingerprint}
            (root / 'client-setup.json').write_text(json.dumps(config))
            self.assertEqual(len(client.load_settings(root, fingerprint)[1]), 64)
            revised = client.updated_hosts('127.0.0.1 localhost\n', config['hosts'])
            self.assertIn('localhost', revised)
            self.assertEqual(revised.count('# awsportal'), 64)
            self.assertEqual(client.updated_hosts(revised, config['hosts']), revised)

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
