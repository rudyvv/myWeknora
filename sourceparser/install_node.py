"""Install only an official, checksum-verified Node runtime for the parser image."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import tempfile
import urllib.request


def install(arch, output):
    root = Path(__file__).resolve().parent
    lock = json.loads((root / 'sfc' / 'runtime.lock.json').read_text(encoding='utf-8'))
    version = lock['node_version']
    checksum = lock['node_archives'].get('linux-' + arch)
    if not checksum or len(checksum) != 64:
        raise ValueError('unsupported locked Node architecture')
    prefix = 'node-v' + version + '-linux-' + arch
    url = 'https://nodejs.org/dist/v' + version + '/' + prefix + '.tar.xz'
    destination = Path(output).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='source-parser-node-') as temporary:
        archive_path = Path(temporary) / 'node.tar.xz'
        digest = hashlib.sha256()
        with urllib.request.urlopen(url, timeout=90) as response, archive_path.open('wb') as archive_file:
            while True:
                block = response.read(1 << 20)
                if not block:
                    break
                digest.update(block)
                archive_file.write(block)
        if digest.hexdigest() != checksum:
            raise ValueError('official Node archive checksum mismatch')
        extracted = Path(temporary) / 'extract'
        extracted.mkdir()
        with tarfile.open(archive_path, 'r:xz') as archive:
            members = []
            for member in archive.getmembers():
                name = PurePosixPath(member.name)
                if name.parts and name.parts[0] == prefix and (
                        name == PurePosixPath(prefix, 'bin', 'node') or
                        name == PurePosixPath(prefix, 'LICENSE') or
                        name.parts[:4] == (prefix, 'lib', 'node_modules', 'npm')):
                    members.append(member)
            required = {PurePosixPath(prefix, 'bin', 'node'), PurePosixPath(prefix, 'LICENSE')}
            if not required.issubset({PurePosixPath(member.name) for member in members}):
                raise ValueError('official Node archive is missing required files')
            archive.extractall(extracted, members=members, filter='data')
        source = extracted / prefix
        (destination / 'bin').mkdir(parents=True, exist_ok=True)
        (destination / 'lib' / 'node_modules').mkdir(parents=True, exist_ok=True)
        shutil.copy2(source / 'bin' / 'node', destination / 'bin' / 'node')
        shutil.copy2(source / 'LICENSE', destination / 'LICENSE')
        shutil.copytree(source / 'lib' / 'node_modules' / 'npm',
                        destination / 'lib' / 'node_modules' / 'npm')
        os.chmod(destination / 'bin' / 'node', 0o755)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--arch', required=True, choices=('x64', 'arm64'))
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    install(args.arch, args.output)


if __name__ == '__main__':
    main()
