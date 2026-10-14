#!/usr/bin/env python3
"""Generate hosts, TLS keys/CSRs and optional certificates on an admin machine."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import subprocess


def load_hosts(path):
    raw = Path(path).read_bytes()
    if len(raw) > 64 * 1024:
        raise ValueError('Hosts file exceeds 64 KiB')
    data = json.loads(raw)
    if not isinstance(data, dict) or set(data) != {'hosts'}:
        raise ValueError('Expected one JSON object containing hosts only')
    hosts = data['hosts']
    if not isinstance(hosts, list) or not 1 <= len(hosts) <= 128:
        raise ValueError('1 to 128 host mappings required')
    result, seen = [], set()
    for h in hosts:
        if not isinstance(h, dict) or set(h) != {'name', 'ip'}:
            raise ValueError('Each mapping requires name and ip only')
        name = h['name'].lower()
        if len(name) > 253 or not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+', name) or name in seen:
            raise ValueError('Invalid or duplicate hostname')
        ip = ipaddress.ip_address(h['ip'])
        if ip.is_unspecified or ip.is_multicast or name.replace('.', '').isdigit():
            raise ValueError('Invalid host IP or hostname')
        seen.add(name)
        result.append({'name': name, 'ip': str(ip)})
    return result


def generate(hosts, out, ca_cert=None, ca_key=None):
    if bool(ca_cert) != bool(ca_key):
        raise ValueError('Both --ca-cert and --ca-key are required for signing')
    # Check the issuer before creating output; use the protected key only locally.
    if ca_cert:
        ca_cert, ca_key = str(Path(ca_cert).resolve()), str(Path(ca_key).resolve())
        subprocess.run(['openssl', 'x509', '-in', ca_cert, '-checkend', '31536000', '-noout'], check=True, capture_output=True)
        info = subprocess.run(['openssl', 'x509', '-in', ca_cert, '-noout', '-text'], check=True, capture_output=True, text=True).stdout
        if 'CA:TRUE' not in info:
            raise ValueError('Issuer certificate must be a CA valid for at least 365 days')
    out = Path(out)
    out.mkdir(mode=0o700, parents=False, exist_ok=False)
    (out / 'hosts.json').write_text(json.dumps({'hosts': hosts}, indent=2) + '\n')
    for h in hosts:
        folder = out / h['name']
        folder.mkdir(mode=0o700)
        key, csr, ext = folder / 'server.key', folder / 'server.csr', folder / 'server.ext'
        ext.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:' + h['name'] + '\n')
        subprocess.run(['openssl', 'req', '-new', '-newkey', 'rsa:3072', '-nodes', '-sha256', '-keyout', str(key), '-out', str(csr), '-subj', '/CN=' + h['name'], '-addext', 'subjectAltName=DNS:' + h['name']], check=True, capture_output=True)
        key.chmod(0o600)
        if ca_cert:
            cert = folder / 'server.crt'
            subprocess.run(['openssl', 'x509', '-req', '-in', str(csr), '-CA', ca_cert, '-CAkey', ca_key, '-set_serial', '0x' + secrets.token_hex(16), '-out', str(cert), '-days', '365', '-sha256', '-extfile', str(ext)], check=True, capture_output=True)
            subprocess.run(['openssl', 'verify', '-CAfile', ca_cert, '-verify_hostname', h['name'], str(cert)], check=True, capture_output=True)
    # No CA key copies, TLS deployment or client trust/hosts changes.
    print(f'Generated {len(hosts)} hosts in {out}. Private keys stay on this admin machine.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--hosts', required=True, help='Approved hosts.json inventory (up to 128 entries)')
    parser.add_argument('--out', required=True, help='New output directory; existing directories are rejected')
    parser.add_argument('--ca-cert', help='Existing public CA PEM; omit with --ca-key for CSR-only mode')
    parser.add_argument('--ca-key', help='Protected local CA key; never upload it to Portal')
    args = parser.parse_args()
    os.umask(0o077)
    try:
        generate(load_hosts(args.hosts), args.out, args.ca_cert, args.ca_key)
    except (ValueError, OSError, TypeError, AttributeError, subprocess.CalledProcessError) as exc:
        # Do not echo subprocess arguments, output or key contents into shared logs.
        parser.exit(1, f'Generation failed ({type(exc).__name__}). Check inventory/CA and inspect any partial output locally; retry with a new directory.\n')


if __name__ == '__main__':
    main()
