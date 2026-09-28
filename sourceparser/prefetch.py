"""Build-time acquisition only; verify the fixed release and lock Java's binary."""
import argparse
import hashlib
import json
from pathlib import Path
import platform

import tree_sitter_language_pack as pack
from runtime import BUNDLES, PACK_VERSION


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cache', required=True)
    args = parser.parse_args()
    cache = Path(args.cache).resolve()
    if cache.exists() and any(cache.iterdir()):
        raise RuntimeError('prefetch requires an empty cache; existing binaries cannot be relocked')
    pack.configure(pack.PackConfig(cache_dir=str(cache)))
    # download() reports the requested count in some wheels without populating
    # the cache. prefetch() must acquire and load the actual Java library.
    pack.prefetch(['java'])
    release = cache / 'tree-sitter-language-pack' / ('v' + PACK_VERSION)
    system = 'windows' if platform.system() == 'Windows' else 'linux'
    machine = 'aarch64' if platform.machine().lower() in ('arm64', 'aarch64') else 'x86_64'
    digest = BUNDLES[system + '-' + machine]
    bundles = list((release / 'bundles').glob('*' + digest + '*.tar.zst'))
    if len(bundles) != 1 or hashlib.sha256(bundles[0].read_bytes()).hexdigest() != digest:
        raise RuntimeError('prefetched archive does not match the fixed release checksum')
    grammars = list((release / 'libs').glob('*tree_sitter_java.*'))
    if len(grammars) != 1:
        raise RuntimeError('Java grammar was not prefetched')
    grammar = grammars[0]
    lock = {'pack_version': PACK_VERSION, 'bundle_sha256': digest,
            'grammar': grammar.relative_to(cache).as_posix(),
            'grammar_sha256': hashlib.sha256(grammar.read_bytes()).hexdigest()}
    (cache / 'grammar.lock.json').write_text(json.dumps(lock, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(lock))


if __name__ == '__main__':
    main()
