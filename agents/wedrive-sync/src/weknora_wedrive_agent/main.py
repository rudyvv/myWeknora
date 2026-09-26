from __future__ import annotations

import argparse
import ctypes
from ctypes import wintypes
import itertools
import json
import logging
import os
import queue
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path

from playwright._impl._errors import TargetClosedError
from websockets.sync.client import connect

from .client import APIClient
from .collector import Collector, FolderSelectionError, ScanFailure
from .security import Identity


ERROR_ALREADY_EXISTS = 183
_single_instance_mutex = None


def app_root() -> Path:
    return Path(os.environ.get("APPDATA", Path.home())) / "WeKnora" / "wedrive-agent"


def configure_logging(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        filename=root / "agent.log",
        encoding="utf-8",
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )


def hide_console() -> bool:
    """Keep the interactive registration prompt, then hide the running Agent."""
    if os.name != "nt":
        return False
    kernel32 = ctypes.windll.kernel32
    user32 = ctypes.windll.user32
    kernel32.GetConsoleWindow.restype = wintypes.HWND
    user32.ShowWindow.argtypes = (wintypes.HWND, ctypes.c_int)
    user32.ShowWindow.restype = ctypes.c_bool
    window = kernel32.GetConsoleWindow()
    if window:
        user32.ShowWindow(window, 0)
        return True
    return False


def acquire_single_instance() -> bool:
    """Allow only one Agent per interactive Windows session."""
    global _single_instance_mutex
    if os.name != "nt":
        return True
    kernel32 = ctypes.windll.kernel32
    kernel32.CreateMutexW.argtypes = (ctypes.c_void_p, wintypes.BOOL, wintypes.LPCWSTR)
    kernel32.CreateMutexW.restype = wintypes.HANDLE
    kernel32.GetLastError.restype = wintypes.DWORD
    kernel32.CloseHandle.argtypes = (wintypes.HANDLE,)
    kernel32.CloseHandle.restype = wintypes.BOOL
    handle = kernel32.CreateMutexW(None, False, r"Local\WeKnoraWeDriveAgent")
    if not handle:
        logging.error("Unable to create the Agent single-instance mutex")
        return False
    if kernel32.GetLastError() == ERROR_ALREADY_EXISTS:
        kernel32.CloseHandle(handle)
        logging.info("Another Agent instance is already running; exiting")
        return False
    _single_instance_mutex = handle
    return True


def install_startup() -> None:
    if os.name != "nt":
        raise RuntimeError("startup registration is supported only on Windows")
    import winreg
    command = f'"{sys.executable}" --background' if getattr(sys, "frozen", False) else f'"{sys.executable}" -m weknora_wedrive_agent.main --background'
    with winreg.CreateKey(winreg.HKEY_CURRENT_USER, r"Software\Microsoft\Windows\CurrentVersion\Run") as key:
        winreg.SetValueEx(key, "WeKnoraWeDriveSync", 0, winreg.REG_SZ, command)


def prompt_registration(default_server: str, default_device_name: str) -> tuple[str, str, str] | None:
    """Collect first-run values with Windows' built-in dialog support.

    The release is a GUI-subsystem executable, so it cannot depend on stdin.
    Values travel through the child environment/stdout and never appear in a
    process command line or log.
    """
    script = r"""
Add-Type -AssemblyName Microsoft.VisualBasic
$title = 'WeKnora 企业微信微盘同步 Agent'
$server = [Microsoft.VisualBasic.Interaction]::InputBox('请输入 WeKnora 服务地址', $title, $env:WEKNORA_DEFAULT_SERVER)
if ([string]::IsNullOrWhiteSpace($server)) { exit 2 }
$code = [Microsoft.VisualBasic.Interaction]::InputBox('请粘贴 RPA 同步页面生成的一次性注册码', $title, '')
if ([string]::IsNullOrWhiteSpace($code)) { exit 2 }
$name = [Microsoft.VisualBasic.Interaction]::InputBox('请输入设备名称', $title, $env:WEKNORA_DEFAULT_DEVICE)
if ([string]::IsNullOrWhiteSpace($name)) { exit 2 }
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::Write((@($server.Trim(), $code.Trim(), $name.Trim()) | ConvertTo-Json -Compress))
"""
    environment = os.environ.copy()
    environment["WEKNORA_DEFAULT_SERVER"] = default_server
    environment["WEKNORA_DEFAULT_DEVICE"] = default_device_name
    try:
        completed = subprocess.run(
            ["powershell.exe", "-NoProfile", "-STA", "-Command", script],
            capture_output=True,
            text=True,
            encoding="utf-8",
            env=environment,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            check=False,
        )
        if completed.returncode != 0:
            return None
        values = json.loads(completed.stdout.strip())
        if not isinstance(values, list) or len(values) != 3 or not all(isinstance(value, str) and value for value in values):
            raise ValueError("registration dialog returned invalid values")
        return values[0], values[1], values[2]
    except Exception:
        logging.exception("Unable to open the first-run registration dialog")
        ctypes.windll.user32.MessageBoxW(None, "无法打开首次注册窗口，请联系 WeKnora 管理员。", "WeKnora Agent", 0x10)
        return None


class Agent:
    def __init__(self, identity: Identity):
        self.identity = identity
        self.api = APIClient(identity)
        self.collector: Collector | None = None
        self.playwright = None
        self.context = None
        # Interactive commands outrank background scans, while preserving FIFO
        # order inside each priority. A scheduled entry can be superseded by a
        # manual request for the same source before it starts.
        self.jobs: queue.PriorityQueue[tuple[int, int, str, dict | None, object | None]] = queue.PriorityQueue()
        self.job_sequence = itertools.count()
        self.send_lock = threading.Lock()
        self.scan_lock = threading.Lock()
        self.pending_scans: dict[str, str] = {}
        self.stop_requested = threading.Event()

    def send(self, ws, kind: str, data: dict) -> None:
        with self.send_lock:
            ws.send(json.dumps({"type": kind, "data": data}, ensure_ascii=False))

    def send_scan_status(self, ws, kind: str, data: dict) -> None:
        if ws is None:
            return
        try:
            self.send(ws, kind, data)
        except Exception:
            # The browser/server socket may disappear while the independent
            # RPA worker is scanning. Status delivery must not abort the scan.
            logging.debug("WeDrive scan status could not be delivered kind=%s", kind)

    def enqueue(self, kind: str, source: dict | None, ws, priority: int = 0) -> None:
        self.jobs.put((priority, next(self.job_sequence), kind, source, ws))

    def ensure_collector(self) -> bool:
        if self.collector is not None:
            if self.collector.is_alive():
                return True
            # Closing the visible Edge window also closes Playwright's
            # persistent context. Do not retain that stale object: otherwise
            # later "login" commands are consumed with no browser to show.
            logging.info("Agent WeDrive browser was closed; launching a fresh session")
            self.dispose_collector()
        try:
            self.playwright, self.context = Collector.launch(self.identity.root / "profile")
        except Exception:
            logging.exception("RPA browser launch failed")
            return False
        self.collector = Collector(self.context, self.identity.root / "profile")
        logging.info("Agent WeDrive browser started")
        return True

    def dispose_collector(self) -> None:
        """Discard a browser session that was closed by the user or Agent."""
        self.collector = None
        context, self.context = self.context, None
        playwright, self.playwright = self.playwright, None
        if context is not None:
            try:
                context.close()
            except Exception:
                logging.debug("Agent browser context was already closed", exc_info=True)
        if playwright is not None:
            try:
                playwright.stop()
            except Exception:
                logging.debug("Agent Playwright runtime was already stopped", exc_info=True)

    def execute_job(self, kind: str, source: dict | None, ws) -> None:
        logging.info("Agent task received: %s", kind)
        if kind == "shutdown":
            self.stop_requested.set()
            self.send(ws, "status", {"state": "stopping"})
            return
        if kind == "select_folder" and self.collector is None:
            if not self.ensure_collector():
                self.send(ws, "error", {"message": "无法启动 Agent 微盘浏览器，请检查 Microsoft Edge 或 Google Chrome"})
                return
            assert self.collector is not None
            restored = self.collector.open_login()
            # Reuse the deployed control-frame vocabulary so an Agent upgrade
            # doesn't require a lock-step backend restart in development.
            self.send(ws, "login_result", {
                "ok": False,
                "reason": "folder_browser_ready",
                "message": "已恢复上次选择的微盘目录；请确认目录后再次点击读取" if restored else "已打开 Agent 微盘浏览器；请进入目标文件夹后再次点击读取",
            })
            return
        if kind == "login":
            # A browser launch or WeDrive navigation can take tens of seconds.
            # Acknowledge before either operation so the web UI never makes a
            # successful click look like it was ignored.
            self.send(ws, "login_result", {
                "ok": False,
                "reason": "login_starting",
                "message": "本机同步工具已开始打开企业微信微盘浏览器。",
            })
            # Page.is_closed() can still report False for a short time after a
            # user closes the Edge window. Treat TargetClosed during the first
            # navigation as a stale session and relaunch once instead of
            # letting the RPA worker consume later login commands silently.
            for attempt in range(2):
                if not self.ensure_collector():
                    self.send(ws, "error", {"message": "无法启动 Agent 微盘浏览器，请检查 Microsoft Edge 或 Google Chrome"})
                    return
                try:
                    self.login(ws)
                    return
                except TargetClosedError:
                    logging.info("Agent WeDrive browser closed during login; relaunching attempt=%s", attempt + 1)
                    self.dispose_collector()
            self.send(ws, "error", {"message": "企业微信微盘浏览器已关闭，重新打开失败。请再点击“登录企业微信”。"})
            return
        elif kind == "select_folder":
            self.select_folder(ws)
        elif kind == "scan" and source:
            self.scan(source, ws, str(source.get("_scan_trigger", "manual")))

    def rpa_loop(self) -> None:
        while True:
            _, _, kind, source, ws = self.jobs.get()
            try:
                self.execute_job(kind, source, ws)
            except Exception:
                # A single interactive browser failure must not kill the only
                # command worker; otherwise every later login click appears
                # to do nothing until the whole Agent is restarted.
                logging.exception("Agent task crashed: %s", kind)
                if ws is not None:
                    self.send(ws, "error", {"message": "本机同步工具任务异常，请再次操作。"})

    def queue_scan(self, source: dict, ws, trigger: str = "scheduled") -> bool:
        """Queue at most one inventory run for a source at a time."""
        source_id = str(source.get("id", ""))
        if not source_id:
            return False
        with self.scan_lock:
            pending = self.pending_scans.get(source_id)
            if pending:
                if trigger == "manual" and pending == "scheduled":
                    # The old scheduled work remains in the priority queue but
                    # becomes a no-op. The manual request is inserted ahead of
                    # other scheduled scans.
                    self.pending_scans[source_id] = "manual"
                    replacement = dict(source)
                    replacement["_scan_trigger"] = "manual"
                    self.enqueue("scan", replacement, ws, priority=0)
                    return True
                logging.info("Ignoring duplicate WeDrive scan request for source_id=%s", source_id)
                return False
            self.pending_scans[source_id] = trigger
        queued = dict(source)
        queued["_scan_trigger"] = trigger
        self.enqueue("scan", queued, ws, priority=0 if trigger == "manual" else 1)
        return True

    def login(self, ws) -> None:
        assert self.collector is not None
        restored = self.collector.open_login()
        self.send(ws, "login_result", {
            "ok": False,
            "reason": "login_browser_opened",
            "message": "已恢复上次选择的微盘目录，正在检查企业微信登录状态。" if restored else "已打开 Agent 微盘浏览器，正在检查企业微信登录状态。",
        })
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            if self.collector.logged_in():
                self.send(ws, "login_result", {"ok": True})
                return
            image = self.collector.qr_data_uri()
            if image:
                self.send(ws, "login_qr", {"image": image})
            time.sleep(1.5)
        self.send(ws, "error", {"message": "企业微信扫码登录超时"})

    def select_folder(self, ws) -> None:
        assert self.collector is not None
        try:
            root_url, name = self.collector.selected_folder()
            self.send(ws, "folder_selected", {"root_url": root_url, "name": name})
        except FolderSelectionError as exc:
            self.send(ws, "error", {"reason": exc.code, "message": str(exc)})
        except Exception as exc:
            self.send(ws, "error", {"reason": "folder_read_failed", "message": str(exc)})

    def scan(self, source: dict, ws=None, trigger: str = "manual") -> None:
        source_id = str(source["id"])
        try:
            with self.scan_lock:
                pending = self.pending_scans.get(source_id)
                if pending is not None and pending != trigger:
                    return
            if not self.api.claim_scan(source_id, trigger):
                logging.info("WeDrive scan not claimed source_id=%s trigger=%s", source_id, trigger)
                return
            if not self.ensure_collector():
                self.api.report_scan_failure(source_id, "browser_unavailable")
                if ws:
                    self.send_scan_status(ws, "error", {"reason": "browser_unavailable", "message": "无法启动 Agent 微盘浏览器，请检查 Microsoft Edge 或 Google Chrome"})
                return
            progress = lambda message: self.send_scan_status(ws, "scan_progress", {"source_id": source_id, "message": message})
            for attempt in range(2):
                try:
                    progress("开始递归扫描微盘目录" if attempt == 0 else "已重新打开 Agent 微盘窗口，正在重试扫描")
                    assert self.collector is not None
                    root_id, items, stats = self.collector.collect(source["root_url"], bool(source.get("auto_share", True)), progress)
                    snapshot_id = self.api.begin_snapshot(source, root_id, len(items))
                    self.api.upload_items(snapshot_id, items)
                    self.api.commit(snapshot_id, len(items), stats)
                    self.send_scan_status(ws, "scan_result", {"source_id": source_id, "item_count": len(items), "complete": True})
                    return
                except TargetClosedError:
                    if attempt == 0:
                        logging.info("Agent browser closed during scan; relaunching source_id=%s", source_id)
                        self.dispose_collector()
                        if self.ensure_collector():
                            continue
                    raise
        except ScanFailure as exc:
            # The details contain only response counters, never URLs, item
            # names, IDs, cookies, or share links. They make a failed live
            # scan actionable without leaking the microdisk's contents.
            logging.error(
                "WeDrive scan failed source_id=%s reason=%s diagnostics=%s",
                source_id, exc.code, exc.diagnostics,
            )
            try:
                self.api.report_scan_failure(source_id, exc.code)
            except Exception:
                logging.exception("Unable to report WeDrive scan failure source_id=%s", source_id)
            if ws:
                self.send_scan_status(ws, "error", {
                    "reason": exc.code,
                    "message": "目录扫描失败：未收到可用的微盘目录列表。请保持 Agent 的微盘窗口登录并刷新目标目录后重试。" if exc.code == "listing_timeout" else "目录扫描失败，请确认 Agent 微盘窗口仍保持登录并重试。",
                })
        except Exception as exc:
            # The concrete error can contain a microdisk URL or share token.
            # Keep that out of WebSocket frames and log only the exception type.
            logging.error("WeDrive scan failed source_id=%s error_type=%s", source_id, type(exc).__name__)
            try:
                self.api.report_scan_failure(source_id, "scan_failed")
            except Exception:
                logging.exception("Unable to report WeDrive scan failure source_id=%s", source_id)
            if ws:
                self.send_scan_status(ws, "error", {
                    "reason": "scan_failed",
                    "message": "目录扫描失败。Agent 已自动尝试恢复微盘窗口；若仍失败，请确认企业微信登录状态后重试。",
                })
        finally:
            with self.scan_lock:
                if self.pending_scans.get(source_id) == trigger:
                    self.pending_scans.pop(source_id, None)

    def scheduled(self, ws, stop: threading.Event) -> None:
        while not stop.is_set():
            try:
                for source in self.api.list_sources():
                    if int(source.get("scan_interval_minutes", 0)) > 0:
                        self.queue_scan(source, ws, "scheduled")
            except Exception as exc:
                self.send(ws, "error", {"message": f"读取同步计划失败：{exc}"})
            stop.wait(60)

    def run(self) -> None:
        path = "/api/v1/wedrive/agent/events"
        threading.Thread(target=self.rpa_loop, daemon=True).start()
        while not self.stop_requested.is_set():
            try:
                with connect(self.api.websocket_url(path), additional_headers=self.api.websocket_headers(path), max_size=768 << 10) as ws:
                    logging.info("Agent websocket connected")
                    stop = threading.Event()
                    threading.Thread(target=self.scheduled, args=(ws, stop), daemon=True).start()
                    try:
                        for raw in ws:
                            frame = json.loads(raw)
                            if frame.get("type") == "login":
                                # This runs in the WebSocket receiver rather
                                # than the single RPA worker. It therefore
                                # remains prompt even while a directory scan
                                # is ahead of the login task in the queue.
                                self.send(ws, "login_result", {
                                    "ok": False,
                                    "reason": "login_queued",
                                    "message": "已收到登录请求，正在等待本机同步工具处理。",
                                })
                                self.enqueue("login", None, ws)
                            elif frame.get("type") == "select_folder":
                                self.enqueue("select_folder", None, ws)
                            elif frame.get("type") == "scan":
                                source_id = str(frame.get("data", {}).get("source_id", ""))
                                source = next((item for item in self.api.list_sources() if item["id"] == source_id), None)
                                if source:
                                    self.queue_scan(source, ws, "manual")
                            elif frame.get("type") == "shutdown":
                                self.execute_job("shutdown", None, ws)
                                break
                    finally:
                        stop.set()
            except Exception:
                logging.exception("Agent websocket connection failed")
                if not self.stop_requested.wait(5):
                    continue
        self.close()

    def close(self) -> None:
        """Release the headed browser before the background Agent exits."""
        self.dispose_collector()
        self.api.http.close()


def main() -> None:
    parser = argparse.ArgumentParser(description="WeKnora enterprise WeDrive RPA Sync Agent")
    parser.add_argument("--server")
    parser.add_argument("--register-code")
    parser.add_argument("--device-name", default=socket.gethostname())
    parser.add_argument("--install-startup", action="store_true")
    parser.add_argument("--background", action="store_true", help="run without an interactive console")
    args = parser.parse_args()
    identity = Identity(app_root())
    configure_logging(identity.root)
    if not acquire_single_instance():
        return
    if not identity.load() and not args.register_code:
        if os.name == "nt" and getattr(sys, "frozen", False):
            registration = prompt_registration(args.server or "", args.device_name)
            if registration is None:
                return
            args.server, args.register_code, args.device_name = registration
        else:
            print("WeKnora 企业微信微盘同步 Agent 首次注册")
            print("请先在 WeKnora 的“RPA 同步”页面点击“注册新设备”。")
            args.server = input("WeKnora 服务地址: ").strip()
            args.register_code = input("一次性注册码: ").strip()
        if not args.server or not args.register_code:
            parser.error("服务地址和一次性注册码不能为空")
        args.install_startup = os.name == "nt"
    if args.register_code:
        if not args.server:
            parser.error("--server is required with --register-code")
        device, key = APIClient.register(args.server, args.register_code, args.device_name)
        identity.create(args.server, device["id"], key)
        logging.info("Device registration completed for %s", device["id"])
    elif not identity.load():
        parser.error("Agent is not registered; provide --server and --register-code")
    # A manually launched newer EXE must replace the old startup target too;
    # browsers often save refreshed downloads with a "(1)" suffix.
    if args.install_startup or (os.name == "nt" and not args.background):
        install_startup()
    hide_console()
    logging.info("Agent started for device %s", identity.device_id)
    Agent(identity).run()


if __name__ == "__main__":
    main()
