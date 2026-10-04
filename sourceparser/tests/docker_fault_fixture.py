"""Run after the HTTP suite inside the isolated, networkless Docker fixture."""
import base64
import errno
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
import unittest
import urllib.error
import urllib.request


PARSER_ROOT = Path(__file__).parents[1]
CACHE = os.environ.get('SOURCE_PARSER_CACHE', '/opt/source-parser/grammar')
sys.path.insert(0, str(PARSER_ROOT))


def request(base_url, path='/health', payload=None, timeout=20):
    data = None if payload is None else json.dumps(payload).encode('utf-8')
    req = urllib.request.Request(base_url + path, data=data,
                                 headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, json.load(response)


def parse_payload(raw=None, chunk_max_bytes=128):
    raw = raw or b'export function reserve() { return "offline"; }\n'
    return {
        'path': 'src/reservations.ts', 'language': 'typescript',
        'sha256': hashlib.sha256(raw).hexdigest(),
        'content_base64': base64.b64encode(raw).decode('ascii'),
        'chunk_max_bytes': chunk_max_bytes,
    }


def start_server():
    environment = dict(os.environ)
    environment['SOURCE_PARSER_CACHE'] = CACHE
    process = subprocess.Popen(
        [sys.executable, str(PARSER_ROOT / 'server.py'),
         '--host', '127.0.0.1', '--port', '0'],
        cwd=PARSER_ROOT, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding='utf-8')
    try:
        import select
        ready, _, _ = select.select([process.stdout], [], [], 90)
        if not ready:
            raise RuntimeError('parser did not announce readiness before the startup deadline')
        line = process.stdout.readline()
        if not line:
            raise RuntimeError('parser exited before readiness: ' + process.stderr.read()[-1000:])
        base_url = 'http://127.0.0.1:' + str(json.loads(line)['port'])
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                status, health = request(base_url)
                if status == 200 and health.get('ready'):
                    return process, base_url, health
            except (OSError, ValueError, urllib.error.URLError):
                time.sleep(0.05)
        raise RuntimeError('parser health did not become ready')
    except BaseException:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)
        raise


def parser_children(parent_pid):
    children = []
    for entry in Path('/proc').iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            status = (entry / 'status').read_text(encoding='ascii')
            ppid = next(int(line.split()[1]) for line in status.splitlines() if line.startswith('PPid:'))
            command = (entry / 'cmdline').read_bytes()
            if ppid == parent_pid and b'multiprocessing.spawn import spawn_main' in command:
                children.append(int(entry.name))
        except (OSError, StopIteration, ValueError):
            continue
    return children


def kill_one_active_worker(process, base_url, sig):
    result = []
    payload = parse_payload()
    thread = threading.Thread(target=lambda: result.append(
        request(base_url, '/v1/parse', payload, timeout=20)), daemon=True)
    thread.start()
    deadline = time.monotonic() + 5
    worker_pid = None
    while time.monotonic() < deadline and thread.is_alive():
        children = parser_children(process.pid)
        if children:
            worker_pid = children[0]
            break
        time.sleep(0.001)
    if worker_pid is None:
        raise RuntimeError('no real parser child process appeared for the fault test')
    if sig == signal.SIGSTOP:
        os.kill(worker_pid, signal.SIGSTOP)
        time.sleep(0.1)
    os.kill(worker_pid, sig)
    thread.join(timeout=20)
    if thread.is_alive():
        raise RuntimeError('HTTP request did not recover after a parser child fault')
    return worker_pid, result[0]


def assert_child_gone(pid):
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.05)
    raise RuntimeError('faulted parser worker remained alive after request cleanup')


def verify_temp_full_isolation(base_url):
    fill_path = Path('/tmp/source-parser-capacity-probe')
    wrote_expected_full = False
    try:
        with fill_path.open('wb', buffering=0) as output:
            block = b'x' * (1 << 20)
            while True:
                output.write(block)
    except OSError as error:
        if error.errno != errno.ENOSPC:
            raise
        wrote_expected_full = True
    finally:
        try:
            fill_path.unlink()
        except FileNotFoundError:
            pass
    if not wrote_expected_full:
        raise RuntimeError('the configured 32 MiB /tmp tmpfs did not reach ENOSPC')
    status, result = request(base_url, '/v1/parse', parse_payload())
    if status != 200 or result.get('quality') not in ('structural', 'partial'):
        raise RuntimeError('parser stopped serving memory-only requests after /tmp filled')


def verify_memory_pressure(base_url):
    pressure_code = (
        'import mmap, time; size=1024*1024*1024; m=mmap.mmap(-1,size); '
        'for_page=range(0,size,4096); '
        '[m.__setitem__(i,1) for i in for_page]; time.sleep(1)')
    result = subprocess.run([sys.executable, '-c', pressure_code],
                            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL, timeout=20, check=False)
    if result.returncode == 0:
        raise RuntimeError('1 GiB memory pressure probe unexpectedly fit the 768 MiB container')
    status, health = request(base_url)
    if status != 200 or not health.get('ready'):
        raise RuntimeError('parser container did not recover from the isolated memory-pressure process')
    status, parsed = request(base_url, '/v1/parse', parse_payload())
    if status != 200 or not parsed.get('chunks'):
        raise RuntimeError('parser did not complete a request after memory pressure')


def stop_server(process):
    if process.poll() is None:
        process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)


def verify_container_limits():
    memory = int(Path('/sys/fs/cgroup/memory.max').read_text(encoding='ascii').strip())
    cpu_quota, cpu_period = map(int, Path('/sys/fs/cgroup/cpu.max').read_text(encoding='ascii').split())
    pids = int(Path('/sys/fs/cgroup/pids.max').read_text(encoding='ascii').strip())
    if memory != 768 * (1 << 20) or (cpu_quota, cpu_period) != (200000, 100000) or pids != 64:
        raise RuntimeError('effective Docker cgroup limits differ from 768 MiB, 2 CPUs and 64 PIDs')

    mounts = {}
    for line in Path('/proc/self/mountinfo').read_text(encoding='utf-8').splitlines():
        before, separator, after = line.partition(' - ')
        if not separator:
            continue
        fields = before.split()
        mountpoint = fields[4].replace('\\040', ' ')
        if mountpoint in ('/', '/tmp', '/contract-cache'):
            mounts[mountpoint] = {'mount_options': fields[5], 'filesystem': after.split()[0],
                                  'super_options': after.split()[2] if len(after.split()) > 2 else ''}
    if '/tmp' not in mounts or mounts['/tmp']['filesystem'] != 'tmpfs':
        raise RuntimeError('the isolated /tmp tmpfs mount is missing')
    if 'noexec' not in mounts['/tmp']['mount_options'].split(','):
        raise RuntimeError('/tmp must remain noexec')
    if '/contract-cache' not in mounts or 'noexec' in mounts['/contract-cache']['mount_options'].split(','):
        raise RuntimeError('the isolated executable contract cache is missing')
    if '/' not in mounts or 'ro' not in mounts['/']['mount_options'].split(','):
        raise RuntimeError('container root filesystem is not read-only')
    return {'memory_limit_bytes': memory, 'cpu_quota_period': [cpu_quota, cpu_period],
            'pids_limit': pids, 'mounts': mounts}


def main():
    container_limits = verify_container_limits()
    if os.environ.get('SOURCE_PARSER_TESTS_DISABLED') != '1':
        test_names = [name for name in os.environ.get('SOURCE_PARSER_TEST_NAMES', '').split(';') if name]
        if test_names:
            loader = unittest.TestLoader()
            suite = unittest.TestSuite(loader.loadTestsFromName(name) for name in test_names)
            result = unittest.TextTestRunner(verbosity=2).run(suite)
            if not result.wasSuccessful():
                return 1
        else:
            suite = subprocess.run(
                [sys.executable, '-m', 'unittest', 'discover', '-s', 'tests', '-v'],
                cwd=PARSER_ROOT, check=False)
            if suite.returncode:
                return suite.returncode

    if os.environ.get('SOURCE_PARSER_FAULTS_DISABLED') == '1':
        print(json.dumps({'unit_tests_only': True, 'container_limits': container_limits}, sort_keys=True))
        return 0

    process, base_url, health = start_server()
    try:
        expected_limits = {
            'max_connections': 8, 'max_request_bytes': 23 << 20,
            'header_timeout_seconds': 10, 'body_timeout_seconds': 10,
            'parse_workers': 2, 'parse_timeout_seconds': 6,
            'max_response_bytes': 32 << 20,
        }
        for name, value in expected_limits.items():
            if health.get('limits', {}).get(name) != value:
                raise RuntimeError('parser did not report the approved finite default: ' + name)
        status, parsed = request(base_url, '/v1/parse', parse_payload())
        if status != 200 or not parsed.get('chunks'):
            raise RuntimeError('networkless parser smoke test failed')

        pid, crashed = kill_one_active_worker(process, base_url, signal.SIGKILL)
        if crashed != (422, {'error': 'source parsing failed'}):
            status, body = crashed
            raise RuntimeError('child crash returned unexpected bounded response: status=' +
                               str(status) + ', error=' + str(body.get('error', 'none')))
        assert_child_gone(pid)
        status, parsed = request(base_url, '/v1/parse', parse_payload())
        if status != 200 or not parsed.get('chunks'):
            raise RuntimeError('parser capacity was not reclaimed after a child crash')

        pid, timed_out = kill_one_active_worker(process, base_url, signal.SIGSTOP)
        if timed_out != (504, {'error': 'source parsing time limit exceeded'}):
            raise RuntimeError('stopped child did not return the configured parse timeout')
        assert_child_gone(pid)

        verify_temp_full_isolation(base_url)
        verify_memory_pressure(base_url)
    finally:
        stop_server(process)

    process, base_url, health = start_server()
    try:
        if not health.get('ready'):
            raise RuntimeError('parser did not recover after a process restart')
        status, parsed = request(base_url, '/v1/parse', parse_payload())
        if status != 200 or not parsed.get('chunks'):
            raise RuntimeError('parser did not parse after a process restart')
    finally:
        stop_server(process)
    print(json.dumps({
        'offline': True, 'container_limits': container_limits,
        'assertions': ['HTTP suite', 'offline parse', 'child crash and slot recovery',
                       'child timeout and reap', 'tmpfs ENOSPC isolation',
                       'memory pressure recovery', 'process restart recovery'],
    }, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
