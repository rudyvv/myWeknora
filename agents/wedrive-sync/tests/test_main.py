from pathlib import Path
from unittest import TestCase
from unittest.mock import MagicMock, patch
import subprocess
import threading

from playwright._impl._errors import TargetClosedError

from weknora_wedrive_agent import main


class FakeIdentity:
    root = Path("profile")
    device_id = "device-1"
    server = "http://127.0.0.1:8080"
    private_key = b"test-key"


class AgentStartupTest(TestCase):
    def test_browser_is_not_started_when_agent_is_constructed(self) -> None:
        with patch.object(main.Collector, "launch") as launch:
            main.Agent(FakeIdentity())  # type: ignore[arg-type]

        launch.assert_not_called()

    def test_ensure_collector_relaunches_after_user_closed_agent_browser(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        stale_collector = MagicMock()
        stale_collector.is_alive.return_value = False
        stale_context = MagicMock()
        stale_playwright = MagicMock()
        agent.collector = stale_collector
        agent.context = stale_context
        agent.playwright = stale_playwright
        replacement_context = MagicMock()
        replacement_playwright = MagicMock()

        with patch.object(main.Collector, "launch", return_value=(replacement_playwright, replacement_context)):
            self.assertTrue(agent.ensure_collector())

        stale_context.close.assert_called_once_with()
        stale_playwright.stop.assert_called_once_with()
        self.assertIs(agent.context, replacement_context)
        self.assertIs(agent.playwright, replacement_playwright)
        self.assertIsNot(agent.collector, stale_collector)

    def test_select_folder_without_browser_opens_agent_browser_with_guidance(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        collector = MagicMock()
        collector.open_login.return_value = False

        def start_collector() -> bool:
            agent.collector = collector
            return True

        with patch.object(agent, "ensure_collector", side_effect=start_collector) as launch:
            agent.execute_job("select_folder", None, ws)

        launch.assert_called_once_with()
        collector.open_login.assert_called_once_with()
        self.assertIn('"type": "login_result"', ws.send.call_args.args[0])
        self.assertIn('"reason": "folder_browser_ready"', ws.send.call_args.args[0])
        self.assertIn("已打开 Agent 微盘浏览器", ws.send.call_args.args[0])

    def test_shutdown_command_marks_agent_for_clean_exit(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()

        agent.execute_job("shutdown", None, ws)

        self.assertTrue(agent.stop_requested.is_set())
        self.assertIn('"state": "stopping"', ws.send.call_args.args[0])

    def test_login_immediately_reports_that_the_agent_browser_is_opening(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        agent.collector = MagicMock()
        agent.api = MagicMock()
        agent.api.claim_scan.return_value = True
        agent.collector.open_login.return_value = False
        agent.collector.logged_in.return_value = True

        agent.login(ws)

        sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
        self.assertIn('"reason": "login_browser_opened"', sent)

    def test_login_acknowledges_before_browser_navigation_finishes(self) -> None:
        """A slow Edge navigation must not make a click appear to do nothing."""
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        navigation_started = threading.Event()
        navigation_release = threading.Event()
        collector = MagicMock()

        def slow_open_login() -> bool:
            navigation_started.set()
            navigation_release.wait(timeout=1)
            return False

        collector.open_login.side_effect = slow_open_login
        collector.logged_in.return_value = True
        agent.collector = collector
        worker = threading.Thread(target=agent.execute_job, args=("login", None, ws), daemon=True)
        worker.start()
        try:
            self.assertTrue(navigation_started.wait(timeout=0.5))
            sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
            self.assertIn('"reason": "login_starting"', sent)
        finally:
            navigation_release.set()
            worker.join(timeout=1)

    def test_login_relaunches_when_a_stale_browser_target_closes_during_navigation(self) -> None:
        """Closing Edge can leave a page object that looks alive until navigation."""
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        stale_collector = MagicMock()
        stale_collector.open_login.side_effect = TargetClosedError("Target page was closed")
        replacement_collector = MagicMock()
        replacement_collector.open_login.return_value = False
        replacement_collector.logged_in.return_value = True
        agent.collector = stale_collector
        ensure_calls = 0

        def ensure() -> bool:
            nonlocal ensure_calls
            ensure_calls += 1
            if ensure_calls == 2:
                agent.collector = replacement_collector
            return True

        with patch.object(agent, "ensure_collector", side_effect=ensure):
            agent.execute_job("login", None, ws)

        self.assertEqual(2, ensure_calls)
        replacement_collector.open_login.assert_called_once_with()
        sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
        self.assertIn('"reason": "login_browser_opened"', sent)

    def test_same_source_is_not_queued_for_scan_twice(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        source = {"id": "source-1"}

        self.assertTrue(agent.queue_scan(source, ws, "scheduled"))
        self.assertFalse(agent.queue_scan(source, ws, "scheduled"))
        _, _, kind, queued_source, queued_ws = agent.jobs.get_nowait()
        self.assertEqual(kind, "scan")
        self.assertEqual(queued_source["id"], source["id"])
        self.assertEqual(queued_source["_scan_trigger"], "scheduled")
        self.assertIs(queued_ws, ws)

    def test_manual_scan_replaces_a_queued_scheduled_scan(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        source = {"id": "source-1"}

        self.assertTrue(agent.queue_scan(source, ws, "scheduled"))
        self.assertTrue(agent.queue_scan(source, ws, "manual"))

        _, _, kind, queued_source, _ = agent.jobs.get_nowait()
        self.assertEqual(kind, "scan")
        self.assertEqual(queued_source["_scan_trigger"], "manual")

    def test_scan_failure_does_not_expose_sensitive_error_text(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        agent.collector = MagicMock()
        agent.api = MagicMock()
        agent.api.claim_scan.return_value = True
        agent.collector.collect.side_effect = RuntimeError("https://drive.weixin.qq.com/s?k=should-not-leak")

        with patch.object(main.logging, "error") as log_error:
            agent.scan({"id": "source-1", "root_url": "https://drive.weixin.qq.com/#/webdisk/folder"}, ws)

        sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
        self.assertIn('"reason": "scan_failed"', sent)
        self.assertNotIn("should-not-leak", sent)
        logged = " ".join(str(value) for value in log_error.call_args.args)
        self.assertNotIn("should-not-leak", logged)

    def test_scan_listing_timeout_reports_a_safe_actionable_reason(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        agent.collector = MagicMock()
        agent.api = MagicMock()
        agent.api.claim_scan.return_value = True
        agent.collector.collect.side_effect = main.ScanFailure(
            "listing_timeout", "https://drive.weixin.qq.com/s?k=must-not-leak", {"candidate_responses": 0},
        )

        with patch.object(main.logging, "error") as log_error:
            agent.scan({"id": "source-1", "root_url": "https://drive.weixin.qq.com/#/webdisk/folder"}, ws)

        sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
        self.assertIn('"reason": "listing_timeout"', sent)
        self.assertNotIn("must-not-leak", sent)
        logged = " ".join(str(value) for value in log_error.call_args.args)
        self.assertIn("listing_timeout", logged)
        self.assertNotIn("must-not-leak", logged)

    def test_scan_reopens_agent_browser_once_after_target_closed(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        stale_collector = MagicMock()
        stale_collector.collect.side_effect = TargetClosedError("Target page, context or browser has been closed")
        fresh_collector = MagicMock()
        fresh_collector.collect.return_value = ("root-1", [{"path": "."}], {"existing": 0, "created": 0, "failed": 0})
        agent.collector = stale_collector
        agent.api = MagicMock()
        agent.api.claim_scan.return_value = True
        agent.api.begin_snapshot.return_value = "snapshot-1"

        def relaunch() -> bool:
            agent.collector = fresh_collector
            return True

        ensure_calls = 0
        def ensure() -> bool:
            nonlocal ensure_calls
            ensure_calls += 1
            return True if ensure_calls == 1 else relaunch()

        with patch.object(agent, "dispose_collector") as dispose, patch.object(agent, "ensure_collector", side_effect=ensure):
            agent.scan({"id": "source-1", "root_url": "https://drive.weixin.qq.com/#/webdisk/folder"}, ws)

        dispose.assert_called_once_with()
        fresh_collector.collect.assert_called_once()
        sent = "\n".join(call.args[0] for call in ws.send.call_args_list)
        self.assertIn('"type": "scan_result"', sent)

    def test_scan_continues_when_progress_socket_disconnects(self) -> None:
        agent = main.Agent(FakeIdentity())  # type: ignore[arg-type]
        ws = MagicMock()
        ws.send.side_effect = OSError("socket closed")
        agent.collector = MagicMock()
        agent.api = MagicMock()
        agent.api.claim_scan.return_value = True
        agent.api.begin_snapshot.return_value = "snapshot-1"

        def collect(_root_url, _auto_share, progress):
            progress("正在扫描子目录")
            return "root-1", [{"path": "."}], {"existing": 0, "created": 0, "failed": 0}

        agent.collector.collect.side_effect = collect
        with patch.object(agent, "ensure_collector", return_value=True):
            agent.scan({"id": "source-1", "root_url": "https://drive.weixin.qq.com/#/webdisk/folder"}, ws)

        agent.api.commit.assert_called_once()
        agent.api.report_scan_failure.assert_not_called()


class ConsoleWindowTest(TestCase):
    def test_hide_console_uses_a_pointer_sized_window_handle(self) -> None:
        kernel32 = MagicMock()
        user32 = MagicMock()
        windows = MagicMock(kernel32=kernel32, user32=user32)
        kernel32.GetConsoleWindow.return_value = 123

        with patch.object(main.os, "name", "nt"), patch.object(main.ctypes, "windll", windows):
            self.assertTrue(main.hide_console())

        self.assertIs(kernel32.GetConsoleWindow.restype, main.wintypes.HWND)
        user32.ShowWindow.assert_called_once_with(123, 0)


class SingleInstanceTest(TestCase):
    def tearDown(self) -> None:
        main._single_instance_mutex = None

    def test_first_agent_instance_acquires_mutex(self) -> None:
        kernel32 = MagicMock()
        kernel32.CreateMutexW.return_value = 456
        kernel32.GetLastError.return_value = 0
        windows = MagicMock(kernel32=kernel32)

        with patch.object(main.os, "name", "nt"), patch.object(main.ctypes, "windll", windows):
            self.assertTrue(main.acquire_single_instance())

    def test_second_agent_instance_is_rejected(self) -> None:
        kernel32 = MagicMock()
        kernel32.CreateMutexW.return_value = 789
        kernel32.GetLastError.return_value = main.ERROR_ALREADY_EXISTS
        windows = MagicMock(kernel32=kernel32)

        with patch.object(main.os, "name", "nt"), patch.object(main.ctypes, "windll", windows):
            self.assertFalse(main.acquire_single_instance())

        kernel32.CloseHandle.assert_called_once_with(789)


class RegistrationDialogTest(TestCase):
    def test_registration_dialog_returns_three_values_without_exposing_them_in_command(self) -> None:
        completed = subprocess.CompletedProcess([], 0, '["http://localhost:5173","one-time-code","PC-1"]', '')
        with patch.object(main.subprocess, "run", return_value=completed) as run:
            result = main.prompt_registration("http://localhost:5173", "PC-1")

        self.assertEqual(result, ("http://localhost:5173", "one-time-code", "PC-1"))
        command = run.call_args.args[0]
        self.assertNotIn("one-time-code", " ".join(command))
