#!/usr/bin/env python3
"""Preview or apply the supplied public CA and hosts mappings. No downloads."""
import argparse
import datetime
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import ssl
import subprocess
import sys
import tempfile


def updated_hosts(text, mappings):
    names = {h['name'].lower() for h in mappings}
    lines = []
    for line in text.splitlines():
        body, mark, comment = line.partition('#')
        fields = body.split()
        if len(fields) >= 2 and any(n.lower() in names for n in fields[1:]):
            aliases = [n for n in fields[1:] if n.lower() not in names]
            line = (fields[0] + '\t' + ' '.join(aliases) if aliases else '')
            if mark and (aliases or comment.strip() != 'awsportal'):
                line += (' ' if line else '') + '#' + comment
            if not line:
                continue
        lines.append(line)
    for h in mappings:
        lines.append(h['ip'] + '\t' + h['name'] + ' # awsportal')
    return '\n'.join(lines) + '\n'


def load_settings(directory, expected):
    config = json.loads((directory / 'client-setup.json').read_text(encoding='utf-8'))
    cert_path = directory / 'ca.crt'
    pem = cert_path.read_text(encoding='ascii')
    if pem.count('-----BEGIN CERTIFICATE-----') != 1 or 'PRIVATE KEY' in pem:
        raise ValueError('One public CA certificate is required')
    fingerprint = hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem)).hexdigest().upper()
    expected = expected.replace(':', '').upper()
    if not re.fullmatch(r'[0-9A-F]{64}', expected) or fingerprint != expected or fingerprint != config['ca_sha256']:
        raise ValueError('CA fingerprint does not match the administrator-provided value')
    output = subprocess.run(['openssl', 'x509', '-in', str(cert_path), '-noout', '-text'], check=True, capture_output=True, text=True).stdout
    if 'CA:TRUE' not in output:
        raise ValueError('Certificate is not a CA')
    dates = subprocess.run(['openssl', 'x509', '-in', str(cert_path), '-noout', '-startdate', '-enddate'], check=True, capture_output=True, text=True).stdout
    decoded = dict(line.split('=', 1) for line in dates.splitlines())
    now = datetime.datetime.now(datetime.timezone.utc).timestamp()
    if not ssl.cert_time_to_seconds(decoded['notBefore']) <= now <= ssl.cert_time_to_seconds(decoded['notAfter']):
        raise ValueError('CA is not currently valid')
    hosts = config['hosts']
    if not 1 <= len(hosts) <= 128:
        raise ValueError('1 to 128 host mappings required')
    seen = set()
    for h in hosts:
        name = h['name']
        if len(name) > 253 or not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+', name) or name in seen:
            raise ValueError('Invalid or duplicate hostname')
        address = ipaddress.ip_address(h['ip'])
        if address.is_unspecified or address.is_multicast:
            raise ValueError('Invalid host IP')
        seen.add(name)
    return cert_path, hosts, fingerprint


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-fingerprint', required=True, help='SHA256 certificate fingerprint received separately from your administrator')
    parser.add_argument('--apply', action='store_true', help='Apply changes; default is preview only')
    args = parser.parse_args()
    directory = Path(__file__).resolve().parent
    cert, hosts, fingerprint = load_settings(directory, args.expected_fingerprint)
    hosts_path = Path('/etc/hosts').resolve()
    original = hosts_path.read_text(encoding='utf-8')
    revised = updated_hosts(original, hosts)
    if sys.platform == 'darwin':
        trust_command = ['security', 'add-trusted-cert', '-d', '-r', 'trustRoot', '-k', '/Library/Keychains/System.keychain', str(cert)]
        trust_path = None
    elif sys.platform.startswith('linux'):
        if Path('/usr/local/share/ca-certificates').is_dir() and shutil.which('update-ca-certificates'):
            trust_path = Path('/usr/local/share/ca-certificates/awsportal-' + fingerprint[:16] + '.crt')
            trust_command = ['update-ca-certificates']
        elif Path('/etc/pki/ca-trust/source/anchors').is_dir() and shutil.which('update-ca-trust'):
            trust_path = Path('/etc/pki/ca-trust/source/anchors/awsportal-' + fingerprint[:16] + '.crt')
            trust_command = ['update-ca-trust', 'extract']
        else:
            raise RuntimeError('Supported system CA store not found (Debian/Ubuntu or RHEL/Fedora required)')
    else:
        raise RuntimeError('Use setup-windows.ps1 on Windows')
    print('CA SHA256:', fingerprint)
    for h in hosts:
        print('hosts:', h['ip'], h['name'])
    print('System CA store will be updated. Existing unrelated hosts aliases are preserved.')
    if not args.apply:
        print('Preview only. Review the values, then run with sudo and --apply.')
        return
    if os.geteuid() != 0:
        raise PermissionError('Run with sudo to apply')
    # Back up before changing trust or hosts. Exclusive creation prevents overwrites.
    backup = hosts_path.with_name('hosts.awsportal-' + datetime.datetime.now().strftime('%Y%m%d%H%M%S%f') + '.bak')
    with backup.open('x', encoding='utf-8') as f:
        f.write(original)
    shutil.copystat(hosts_path, backup)
    if trust_path is not None:
        trust_path.write_bytes(cert.read_bytes())
        trust_path.chmod(0o644)
    subprocess.run(trust_command, check=True)
    stat = hosts_path.stat()
    fd, pending = tempfile.mkstemp(prefix='.awsportal-hosts-', dir=str(hosts_path.parent))
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as f:
            f.write(revised)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(pending, stat.st_mode & 0o777)
        os.chown(pending, stat.st_uid, stat.st_gid)
        os.replace(pending, hosts_path)
    finally:
        if os.path.exists(pending):
            os.unlink(pending)
    if sys.platform == 'darwin':
        subprocess.run(['dscacheutil', '-flushcache'], check=True)
        subprocess.run(['killall', '-HUP', 'mDNSResponder'], check=True)
    print('Applied. Hosts backup:', backup)
    print('Restart your browser and open the administrator-provided HTTPS hostname.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError, KeyError) as error:
        sys.exit(str(error))
