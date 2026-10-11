"""Root-only persistent storage and immutable dataset cache operations."""
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tempfile


def relative_path(raw):
    path = pathlib.PurePosixPath(raw)
    if not raw or raw in ('.','..') or raw != path.as_posix() or path.is_absolute() or '..' in path.parts or '\\' in raw:
        raise ValueError('unsafe dataset path')
    return path


def validate_manifest(manifest):
    if manifest.get('version') != 1 or not manifest.get('files'):
        raise ValueError('versioned nonempty manifest required')
    seen = set()
    for f in manifest['files']:
        path = relative_path(f['path'])
        if str(path) in seen or type(f['size']) is not int or f['size'] < 0:
            raise ValueError('duplicate or invalid file')
        seen.add(str(path))
        if len(f.get('sha256', '')) != 64 or any(c not in '0123456789abcdef' for c in f['sha256']) or not f.get('version_id') or f['version_id'] == 'null':
            raise ValueError('S3 version and SHA256 required; ETag is not a checksum')
    for path in seen:
        if any(str(p) in seen for p in pathlib.PurePosixPath(path).parents if str(p) != '.'):
            raise ValueError('file/directory collision')


def file_matches(path, expected):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != expected['size']:
        return False
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(4 * 1024 * 1024), b''):
            h.update(block)
    return h.hexdigest() == expected['sha256']


def cache_dataset(root, dataset_id, manifest, download):
    """download(file,destination) must fetch the exact approved S3 object version.
    Staging is never visible as AVAILABLE. Retrying verifies each prior file.
    """
    validate_manifest(manifest)
    if len(dataset_id) != 64 or any(c not in '0123456789abcdef' for c in dataset_id):
        raise ValueError('dataset identity must be manifest SHA256')
    root = pathlib.Path(root)
    root.mkdir(parents=True, exist_ok=True, mode=0o755)
    canonical = json.dumps(manifest, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
    if hashlib.sha256(canonical).hexdigest() != dataset_id:
        raise ValueError('manifest identity changed')
    available, staging = root / dataset_id, root / ('.' + dataset_id + '.pending')
    if available.exists():
        if all(file_matches(available / f['path'], f) for f in manifest['files']):
            return available
        raise RuntimeError('AVAILABLE cache corrupted')
    staging.mkdir(mode=0o700, exist_ok=True)
    if staging.is_symlink():
        raise RuntimeError('symlink staging forbidden')
    busy = root / ('.' + dataset_id + '.busy')
    busy.write_text(dataset_id)
    try:
        remaining = sum(f['size'] for f in manifest['files'] if not file_matches(staging / f['path'], f))
        if shutil.disk_usage(root).free < remaining + 1024 * 1024 * 1024:
            raise RuntimeError('insufficient cache capacity')
        for f in manifest['files']:
            path = staging / f['path']
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
            if any(p.is_symlink() for p in path.parents if p != root.parent):
                raise RuntimeError('symlink parent forbidden')
            if not file_matches(path, f):
                temporary = path.with_name(path.name + '.downloading')
                download(f, temporary)
                if not file_matches(temporary, f):
                    raise RuntimeError('dataset checksum/size mismatch')
                os.replace(temporary, path)
            path.chmod(0o444)
        expected = {f['path'] for f in manifest['files']}
        actual = {p.relative_to(staging).as_posix() for p in staging.rglob('*') if p.is_file()}
        if actual != expected:
            raise RuntimeError('unexpected files in cache')
        for path in sorted(staging.rglob('*'), reverse=True):
            if path.is_dir():
                path.chmod(0o555)
        staging.chmod(0o555)
        os.sync()
        os.replace(staging, available)
        return available
    finally:
        busy.unlink(missing_ok=True)


def migrate_home(source, destination, user_id, offline_proof, run=subprocess.run):
    """Offline copy + checksum verification. The source is always retained.
    Caller must hold the Portal HOME migration lease and stop user sessions/jobs.
    """
    if type(user_id) is not int or not 0 < user_id <= 1000000 or not offline_proof:
        raise ValueError('stable user ID and stopped source evidence required')
    ownership = '--chown='+str(200000+user_id)+':'+str(200000+user_id)
    source, destination = pathlib.Path(source), pathlib.Path(destination)
    if source.is_symlink() or destination.is_symlink() or not source.is_dir() or not destination.is_dir():
        raise ValueError('real mounted directories required')
    if source.resolve() == destination.resolve():
        raise ValueError('distinct source and destination required')
    run(['/usr/bin/rsync','-aHAX','--numeric-ids',ownership,str(source)+'/',str(destination)+'/'], check=True)
    verification = run(['/usr/bin/rsync','-aHAXnc','--numeric-ids','--itemize-changes',str(source)+'/',str(destination)+'/'], check=True, capture_output=True, text=True)
    if verification.stdout.strip():
        raise RuntimeError('HOME copy verification failed; original retained')
    os.sync()
    return {'verified':True,'source_retained':True,'source':str(source),'destination':str(destination),'user_id':user_id,'offline_proof':offline_proof}


def scratch_quota(root, user_id, limit_gib, run=subprocess.run):
    if type(user_id) is not int or not 0 < user_id <= 1000000 or type(limit_gib) is not int or not 1 <= limit_gib <= 4096:
        raise ValueError('approved numeric user/quota required')
    root = pathlib.Path(root)
    result = run(['/usr/bin/findmnt','--mountpoint',str(root),'-n','-o','FSTYPE,OPTIONS'],check=True,capture_output=True,text=True)
    if not result.stdout.startswith('xfs ') or not any(x in result.stdout for x in ('prjquota','pquota')):
        raise RuntimeError('XFS project quota mount required')
    path = root / ('awp-u' + str(user_id))
    path.mkdir(mode=0o700, exist_ok=True)
    if path.is_symlink():
        raise RuntimeError('symlink scratch forbidden')
    os.chown(path,200000+user_id,200000+user_id)
    (path/'tmp').mkdir(mode=0o700,exist_ok=True)
    os.chown(path/'tmp',200000+user_id,200000+user_id)
    project = 1000 + user_id
    run(['/usr/sbin/xfs_quota','-x','-c',f'project -s -p {path} {project}',str(root)],check=True)
    run(['/usr/sbin/xfs_quota','-x','-c',f'limit -p bsoft={limit_gib}g bhard={limit_gib}g {project}',str(root)],check=True)
    return str(path)


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description='Offline HOME copy; never removes original')
    parser.add_argument('source'); parser.add_argument('destination')
    parser.add_argument('--user-id', type=int, required=True)
    parser.add_argument('--offline-proof', required=True)
    arguments = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit('approved root migration host required')
    print(json.dumps(migrate_home(arguments.source, arguments.destination, arguments.user_id, arguments.offline_proof)))
