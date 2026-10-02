#!/usr/bin/env python3
"""Automation client for registered read-only awsportal Git mirrors."""
import argparse
import json
import os
from pathlib import Path
import ssl
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', default=os.environ.get('AWSPORTAL_MIRROR_CONFIG', str(Path.home() / '.config/awsportal/mirror.json')))
    commands = parser.add_subparsers(dest='command', required=True)
    sync = commands.add_parser('sync', help='request upstream sync by registered mirror ID')
    sync.add_argument('repo_id', type=int)
    sync.add_argument('--wait', action='store_true')
    sync.add_argument('--timeout', type=int, default=900)
    status = commands.add_parser('status', help='show job state')
    status.add_argument('job_id', type=int)
    for command in ('clone', 'fetch'):
        p = commands.add_parser(command)
        p.add_argument('repo_id', type=int)
        p.add_argument('directory')
    args = parser.parse_args()
    path = Path(args.config)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise RuntimeError('config must be a private regular file (chmod 600)')
    config = json.loads(path.read_text())
    portal = config['portal_url'].rstrip('/')
    parsed = urllib.parse.urlparse(portal)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username is not None or parsed.query or parsed.fragment:
        raise RuntimeError('portal_url must be an HTTPS URL without credentials or query')
    token = config['token']
    if not isinstance(token, str) or not token or any(c in token for c in '\r\n'):
        raise RuntimeError('invalid token')
    context = ssl.create_default_context(cafile=config.get('ca_file'))

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    opener = urllib.request.build_opener(NoRedirect(), urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))

    class Cooldown(RuntimeError):
        def __init__(self, seconds):
            super().__init__('sync cooldown; retry later')
            self.seconds = seconds

    def request(endpoint, method='GET'):
        req = urllib.request.Request(portal + endpoint, data=b'' if method == 'POST' else None,
                                     headers={'Authorization': 'Bearer ' + token}, method=method)
        try:
            with opener.open(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            if error.code == 429:
                value = error.headers.get('Retry-After', '30')
                seconds = int(value) if value.isdigit() else 30
                raise Cooldown(max(1, min(seconds, 300))) from None
            raise RuntimeError(f'portal returned HTTP {error.code}') from None

    if args.command == 'status':
        if args.job_id < 1:
            raise RuntimeError('job ID must be positive')
        print(json.dumps(request(f'/api/mirror-jobs/{args.job_id}'), ensure_ascii=False))
        return
    if args.repo_id < 1:
        raise RuntimeError('repository ID must be positive')
    if args.command == 'sync':
        if args.timeout < 1:
            raise RuntimeError('timeout must be positive')
        deadline = time.monotonic() + args.timeout
        while True:
            try:
                job = request(f'/api/mirrors/{args.repo_id}/sync', 'POST')
                break
            except Cooldown as error:
                if not args.wait:
                    raise
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise RuntimeError('sync request timed out')
                time.sleep(min(error.seconds, remaining))
        print(json.dumps(job, ensure_ascii=False), flush=True)
        if not args.wait:
            return
        while time.monotonic() < deadline:
            state = request(f'/api/mirror-jobs/{job["job_id"]}')
            if state['state'] == 'success':
                print(json.dumps(state, ensure_ascii=False))
                return
            if state['state'] == 'error':
                raise RuntimeError(state.get('message') or 'sync failed')
            time.sleep(3)
        raise RuntimeError('sync wait timed out; job continues on portal')

    url = portal + f'/git/mirrors/{args.repo_id}'
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(('GIT_', 'SSH_')) and not key.upper().endswith('PROXY')}
    settings = {f'http.{url}.extraHeader': 'Authorization: Bearer ' + token, 'http.followRedirects': 'false',
                'http.sslVerify': 'true', 'credential.helper': '', 'protocol.allow': 'never',
                'protocol.https.allow': 'always', 'submodule.recurse': 'false', 'fetch.recurseSubmodules': 'false'}
    if config.get('ca_file'):
        settings['http.sslCAInfo'] = config['ca_file']
    env.update(GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_SYSTEM='/dev/null', GIT_TERMINAL_PROMPT='0',
               GIT_CONFIG_COUNT=str(len(settings)))
    for i, (key, value) in enumerate(settings.items()):
        env[f'GIT_CONFIG_KEY_{i}'] = key
        env[f'GIT_CONFIG_VALUE_{i}'] = value
    if args.command == 'clone':
        command = ['git', 'clone', '--', url, args.directory]
    else:
        remote = subprocess.check_output(['git', '-C', args.directory, 'remote', 'get-url', 'origin'], env=env, text=True).strip()
        if remote != url:
            raise RuntimeError('origin differs from registered portal mirror URL')
        command = ['git', '-C', args.directory, 'fetch', '--prune', 'origin']
    subprocess.run(command, env=env, check=True)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.CalledProcessError) as error:
        print(f'awsportal-mirror: {error}', file=sys.stderr)
        sys.exit(1)
