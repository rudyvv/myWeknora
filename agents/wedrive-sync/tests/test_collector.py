from pathlib import Path
from tempfile import TemporaryDirectory
from unittest import TestCase
from unittest.mock import patch
import pytest

from weknora_wedrive_agent.collector import (
    Collector,
    FolderSelectionError,
    ScanFailure,
    _browser_captured_share_url,
    _click_virtualized_text,
)
from weknora_wedrive_agent.client import ScanAttemptConflict


class FakePage:
    def __init__(self, url: str, title: str = "项目 A") -> None:
        self.url = url
        self._title = title

    def on(self, *_args) -> None:
        pass

    def bring_to_front(self) -> None:
        pass

    def goto(self, url: str, **_kwargs) -> None:
        self.url = url

    def reload(self, **_kwargs) -> None:
        pass

    def is_closed(self) -> bool:
        return False

    def title(self) -> str:
        return self._title


class StaleURLPage(FakePage):
    """Models a SPA whose automation URL lags behind location.href."""

    def __init__(self, url: str, live_url: str, title: str = "项目 A") -> None:
        super().__init__(url, title)
        self._live_url = live_url

    def evaluate(self, _expression: str) -> str:
        return self._live_url


class BrowserCapturePage(FakePage):
    def evaluate(self, _expression: str, *_args) -> str:
        return "https://drive.weixin.qq.com/s?k=share-token"


class BreadcrumbPage(FakePage):
    def evaluate(self, expression: str, *_args) -> str:
        if 'breadcrumb' in expression:
            return 'EVIP › EVIP系统业务规则梳理 › 接口文档'
        return self.url


class SharePage(FakePage):
    def __init__(self, url: str) -> None:
        super().__init__(url)
        self.keyboard = type("Keyboard", (), {"press": lambda *_args, **_kwargs: None})()

    def wait_for_timeout(self, _milliseconds: int) -> None:
        pass


class VirtualizedLocator:
    def __init__(self, page: "VirtualizedPage") -> None:
        self.page = page

    def count(self) -> int:
        return int(self.page.row_exposed)

    def nth(self, _index: int) -> "VirtualizedLocator":
        return self

    def is_visible(self) -> bool:
        return self.page.row_exposed

    def click(self, **_kwargs) -> None:
        self.page.clicked = True


class VirtualizedPage(FakePage):
    """A row exists only after the virtual list has been scrolled."""

    def __init__(self, url: str) -> None:
        super().__init__(url)
        self.row_exposed = False
        self.clicked = False
        self.keyboard = type("Keyboard", (), {"press": lambda *_args, **_kwargs: None})()

    def get_by_role(self, *_args, **_kwargs) -> VirtualizedLocator:
        return VirtualizedLocator(self)

    def get_by_text(self, *_args, **_kwargs) -> VirtualizedLocator:
        return VirtualizedLocator(self)

    def evaluate(self, expression: str, *_args):
        if "scrollBy" in expression:
            self.row_exposed = True
            return {"moved": True, "at_end": False}
        if "lastShareURL" in expression:
            return "https://drive.weixin.qq.com/s?k=captured-token"
        return {"moved": True, "at_end": False}

    def wait_for_timeout(self, _milliseconds: int) -> None:
        pass


class FakeContext:
    def __init__(self, *pages: FakePage) -> None:
        self.pages = list(pages)

    def add_init_script(self, *_args) -> None:
        pass


class CachedChildPage(FakePage):
    """A mounted child route emits its list only after a fresh document boot."""
    clock = 0.0
    emit_child_listing = True
    child_body = {"file_list": []}
    child_head = {"ret": 0}
    child_request_id = "child"

    def on(self, event, callback):
        if event == "response":
            self.response_callback = callback

    def evaluate(self, _expression, *_args):
        return {"moved": False, "at_end": True}

    def wait_for_timeout(self, milliseconds):
        self.clock += milliseconds / 1000

    def goto(self, url, **_kwargs):
        self.url = url
        if "folderid=child" in url:
            if "weknora_inventory_nonce=" in url and self.emit_child_listing:
                self.response_callback(FakeResponse({"head": self.child_head, "body": self.child_body}, {"father_id": self.child_request_id}))
        else:
            self.response_callback(FakeResponse({"body": {"file_list": [
                {"name": "child", "file_id": "child", "father_id": "root", "file_type": 1}
            ]}}))


def test_collect_retries_cached_child_with_fresh_validated_listing(tmp_path):
    root = "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/space-id?folderid=root"
    page = CachedChildPage(root)
    collector = Collector(FakeContext(page), tmp_path / "profile")
    with patch("weknora_wedrive_agent.collector.time.monotonic", side_effect=lambda: page.clock):
        root_id, items, _stats = collector.collect(root, False)
    assert root_id == "root"
    assert [(item["path"], item["external_id"]) for item in items] == [(".", "root"), ("child", "child")]


def test_child_retry_cannot_accept_unverified_inventory(tmp_path):
    root = "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/space-id?folderid=root"
    page = CachedChildPage(root)
    page.emit_child_listing = False
    collector = Collector(FakeContext(page), tmp_path / "profile")
    with patch("weknora_wedrive_agent.collector.time.monotonic", side_effect=lambda: page.clock):
        with pytest.raises(ScanFailure) as failure:
            collector.collect(root, False)
    assert failure.value.code == "tree_walk_failed"


def test_collect_accepts_successful_empty_child_without_file_list(tmp_path):
    root = "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/space-id?folderid=root"
    page = CachedChildPage(root)
    # Observed WeCom empty-folder response: success + exhausted pagination,
    # with file_list omitted rather than present as an empty array.
    page.child_body = {"next_start": 0, "has_next": False}
    collector = Collector(FakeContext(page), tmp_path / "profile")
    with patch("weknora_wedrive_agent.collector.time.monotonic", side_effect=lambda: page.clock):
        root_id, items, _stats = collector.collect(root, False)
    assert root_id == "root"
    assert [(item["path"], item["item_type"]) for item in items] == [(".", "folder"), ("child", "folder")]


@pytest.mark.parametrize("head,body,request_id", [
    ({"ret": 403}, {"next_start": 0, "has_next": False}, "child"),
    ({}, {"next_start": 0, "has_next": False}, "child"),
    ({"ret": False}, {"next_start": 0, "has_next": False}, "child"),
    ({"ret": 0}, {"next_start": 0, "has_next": True}, "child"),
    ({"ret": 0}, {"next_start": 1, "has_next": False}, "child"),
    ({"ret": 0}, {"next_start": False, "has_next": False}, "child"),
    ({"ret": 0}, {"next_start": 0, "has_next": False, "file_list": None}, "child"),
    ({"ret": 0}, {"next_start": 0, "has_next": False}, "other-folder"),
])
def test_empty_child_requires_success_exhausted_page_and_matching_id(tmp_path, head, body, request_id):
    root = "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/space-id?folderid=root"
    page = CachedChildPage(root)
    page.child_head, page.child_body, page.child_request_id = head, body, request_id
    collector = Collector(FakeContext(page), tmp_path / "profile")
    with patch("weknora_wedrive_agent.collector.time.monotonic", side_effect=lambda: page.clock):
        with pytest.raises(ScanFailure) as failure:
            collector.collect(root, False)
    assert failure.value.code == "tree_walk_failed"


class FakeBrowserType:
    def __init__(self) -> None:
        self.kwargs = {}

    def launch_persistent_context(self, _profile_root: str, **kwargs) -> FakeContext:
        self.kwargs = kwargs
        return FakeContext(FakePage("about:blank"))


class FakePlaywright:
    def __init__(self) -> None:
        self.chromium = FakeBrowserType()

    def stop(self) -> None:
        pass


class FakePlaywrightStarter:
    def __init__(self, playwright: FakePlaywright) -> None:
        self.playwright = playwright

    def start(self) -> FakePlaywright:
        return self.playwright


class FakeResponse:
    def __init__(self, payload: dict, request_payload: dict | None = None, status: int = 200) -> None:
        self.url = "https://drive.weixin.qq.com/webdisk/list"
        self.status = status
        self._payload = payload
        self.request = type("Request", (), {"post_data_json": request_payload, "post_data": None})()

    def json(self) -> dict:
        return self._payload


class ListingPage(FakePage):
    def on(self, event, callback) -> None:
        if event == "response":
            self.response_callback = callback

    def evaluate(self, _expression, *_args):
        return {"moved": False, "at_end": True}

    def wait_for_timeout(self, _milliseconds):
        pass

    def goto(self, url, **_kwargs):
        self.url = url
        # Wrong folder and malformed payload are not confirmed progress.
        self.response_callback(FakeResponse({"body": {"file_list": []}}, {"father_id": "other"}))
        self.response_callback(FakeResponse({"errcode": 1}, {"father_id": "folder-1"}))
        self.response_callback(FakeResponse({"body": {"file_list": [{"name": "error.txt", "file_id": "error-only", "father_id": "folder-1"}]}}, {"father_id": "folder-1"}, status=500))
        payload = {"body": {"file_list": [{"name": "file.txt", "file_id": "file-1", "father_id": "folder-1"}]}}
        response = FakeResponse(payload, {"father_id": "folder-1"})
        self.response_callback(response)
        self.response_callback(response)  # A duplicate network response.


class CancelledParentPage(ListingPage):
    lost = False

    def __init__(self, url):
        super().__init__(url)
        self.navigations = []

    def goto(self, url, **_kwargs):
        self.url = url
        self.navigations.append(url)
        if len(self.navigations) == 3:
            self.lost = True
            return
        parent = "child" if len(self.navigations) == 2 else "folder-1"
        files = [] if parent == "child" else [{"name": "child", "file_id": "child", "father_id": "folder-1", "file_type": 1}]
        self.response_callback(FakeResponse({"body": {"file_list": files}}, {"father_id": parent}))

    def wait_for_timeout(self, _milliseconds):
        if self.lost:
            raise RuntimeError("navigation interrupted")


class SelectedFolderTest(TestCase):
    FOLDER_URL = "https://drive.weixin.qq.com/#/webdisk/folder?id=folder-1"
    CGI_FOLDER_URL = "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.1970325030004462.772095854VR0/all?folderid=s.1970325030004462.772095854VR0_d.7724"
    HOME_URL = "https://drive.weixin.qq.com/#/"

    def setUp(self) -> None:
        self._directory = TemporaryDirectory()

    def test_collect_progress_only_counts_validated_unique_facts(self) -> None:
        page = ListingPage(self.FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)
        events = []
        root, items, _stats = collector.collect(self.FOLDER_URL, False, on_progress=lambda: events.append("progress"))
        self.assertEqual("folder-1", root)
        self.assertEqual(2, len(items))
        self.assertEqual(3, len(events), "one listing, one new file, one validated root")

    def test_lost_lease_during_parent_navigation_stops_fallbacks(self) -> None:
        page = CancelledParentPage(self.FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)
        def checkpoint():
            if page.lost:
                raise ScanAttemptConflict()
        with self.assertRaises(ScanAttemptConflict):
            collector.collect(self.FOLDER_URL, False, checkpoint=checkpoint)
        self.assertEqual(3, len(page.navigations), "no fresh navigation after lease loss")

    def tearDown(self) -> None:
        self._directory.cleanup()

    @property
    def profile_root(self) -> Path:
        return Path(self._directory.name) / "profile"

    def collector(self, url: str) -> Collector:
        return Collector(FakeContext(FakePage(url)), self.profile_root)  # type: ignore[arg-type]

    def test_accepts_authenticated_webdisk_folder_page(self) -> None:
        url = self.FOLDER_URL

        selected_url, name = self.collector(url).selected_folder()

        self.assertEqual(selected_url, url)
        self.assertEqual(name, "项目 A")

    def test_accepts_current_cgi_space_folder_page(self) -> None:
        url = self.CGI_FOLDER_URL

        selected_url, name = self.collector(url).selected_folder()

        self.assertEqual(selected_url, url)
        self.assertEqual(name, "项目 A")

    def test_selected_folder_prefers_visible_breadcrumb_over_generic_page_title(self) -> None:
        page = BreadcrumbPage(self.CGI_FOLDER_URL, title="企业微信·微盘")
        collector = Collector(FakeContext(page), self.profile_root)  # type: ignore[arg-type]

        _url, name = collector.selected_folder()

        self.assertEqual(name, "EVIP › EVIP系统业务规则梳理 › 接口文档")

    def test_reads_location_href_when_spa_page_url_is_stale(self) -> None:
        # The live WeCom SPA can update the address bar hash without the
        # Playwright Page.url snapshot observing it.  Folder selection must
        # use the document's current location, otherwise a real folder is
        # misreported as the space home page.
        stale = StaleURLPage(
            "https://drive.weixin.qq.com/webdisk/index?t=home",
            self.CGI_FOLDER_URL,
        )
        collector = Collector(FakeContext(stale), self.profile_root)  # type: ignore[arg-type]

        selected_url, _ = collector.selected_folder()

        self.assertEqual(selected_url, self.CGI_FOLDER_URL)

    def test_rejects_current_cgi_space_root_without_folderid(self) -> None:
        space_root = "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/i.1970325030004462.1688854293012816/all"

        with self.assertRaises(FolderSelectionError) as raised:
            self.collector(space_root).selected_folder()

        self.assertEqual(raised.exception.code, "folder_not_selected")

    def test_collect_reports_listing_timeout_with_safe_diagnostics(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector._wait_listing = lambda *_args: False  # type: ignore[method-assign]

        with self.assertRaises(ScanFailure) as raised:
            collector.collect(self.CGI_FOLDER_URL, auto_share=False)

        self.assertEqual(raised.exception.code, "listing_timeout")
        self.assertEqual(raised.exception.diagnostics, {
            "candidate_responses": 0,
            "json_responses": 0,
            "list_responses": 0,
            "accepted_listings": 0,
            "enter_click_failed": 0,
            "enter_listing_timeout": 0,
            "enter_fresh_attempts": 0,
            "return_navigation_failed": 0,
            "return_cache_verified": 0,
            "return_direct_attempts": 0,
            "return_fresh_attempts": 0,
            "direct_folder_navigation": 0,
            "direct_navigation_failed": 0,
        })

    def test_collect_retries_with_a_second_fresh_navigation_after_boot_failure(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        gotos: list[str] = []
        original_goto = collector.page.goto
        collector.page.goto = lambda url, **kwargs: (gotos.append(url), original_goto(url, **kwargs))  # type: ignore[method-assign]
        calls = iter((False, True))
        collector._wait_listing = lambda *_args: next(calls)  # type: ignore[method-assign]
        collector._walk = lambda *_args: None  # type: ignore[method-assign]

        root_id, _items, _stats = collector.collect(self.CGI_FOLDER_URL, auto_share=False)

        self.assertEqual(root_id, "s.1970325030004462.772095854VR0_d.7724")
        self.assertEqual(len(gotos), 2)
        self.assertTrue(all("weknora_inventory_nonce=" in url for url in gotos))

    def test_collect_forces_a_fresh_document_navigation_after_cached_listing_timeout(self) -> None:
        """A WeCom SPA reload can retain its cached folder payload.

        The scan must make one fresh document navigation with a nonce before
        declaring `listing_timeout`; otherwise a visible, logged-in folder can
        never produce the protocol-backed inventory required for a snapshot.
        """
        page = FakePage(self.CGI_FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)  # type: ignore[arg-type]
        gotos: list[str] = []
        original_goto = page.goto

        def capture_goto(url: str, **kwargs) -> None:
            gotos.append(url)
            original_goto(url, **kwargs)

        page.goto = capture_goto  # type: ignore[method-assign]
        calls = iter((False, True))
        collector._wait_listing = lambda *_args: next(calls)  # type: ignore[method-assign]
        collector._walk = lambda *_args: None  # type: ignore[method-assign]

        collector.collect(self.CGI_FOLDER_URL, auto_share=False)

        self.assertEqual(len(gotos), 2)
        self.assertIn("weknora_inventory_nonce=", gotos[-1])

    def test_collect_ignores_stale_listing_from_previous_folder(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        root_id = collector._id_from_url(self.CGI_FOLDER_URL)
        original_goto = collector.page.goto

        def receive_stale_then_current(url: str, **kwargs) -> None:
            original_goto(url, **kwargs)
            collector._on_response(FakeResponse({"body": {"file_list": [
                {"name": "stale.txt", "file_id": "stale-file", "father_id": "previous-folder"},
            ]}}))
            collector._on_response(FakeResponse({"body": {"file_list": [
                {"name": "current.txt", "file_id": "current-file", "father_id": root_id},
            ]}}))

        collector.page.goto = receive_stale_then_current  # type: ignore[method-assign]
        collector._walk = lambda *_args: None  # type: ignore[method-assign]

        scanned_root, items, _stats = collector.collect(self.CGI_FOLDER_URL, auto_share=False)

        self.assertEqual(scanned_root, root_id)
        self.assertEqual({item["external_id"] for item in items}, {root_id, "current-file"})
        self.assertEqual(collector.diagnostics["accepted_listings"], 1)

    def test_listing_uses_only_direct_body_file_list(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.expected_id = "root"
        response = FakeResponse({"body": {
            "file_list": [{"name": "direct", "file_id": "child", "father_id": "root", "file_type": 1}],
            "metadata": {"file_list": [
                {"name": "wrong-a", "file_id": "wrong-a", "father_id": "wrong", "file_type": 1},
                {"name": "wrong-b", "file_id": "wrong-b", "father_id": "wrong", "file_type": 1},
            ]},
        }})

        collector._on_response(response)

        self.assertEqual(collector.current_parent_id, "root")
        self.assertEqual(collector.children[""], [("direct", "child")])

    def test_empty_listing_uses_request_folder_id_for_navigation_validation(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.expected_id = "child"

        collector._on_response(FakeResponse({"body": {"file_list": []}}, {"query": {"father_id": "child"}}))

        self.assertEqual(collector.current_parent_id, "child")
        self.assertEqual(collector.accepted_seq, 1)

    def test_return_to_cached_parent_is_valid_without_a_new_listing_request(self) -> None:
        # WeCom can restore a breadcrumb destination from its SPA cache.  The
        # parent list is already visible, but no new /webdisk/list response is
        # emitted.  Returning must not abort an otherwise complete DFS.
        collector = self.collector(self.CGI_FOLDER_URL)
        collector._click_visible = lambda *_args, **_kwargs: True  # type: ignore[method-assign]
        collector._wait_listing = lambda *_args, **_kwargs: False  # type: ignore[method-assign]
        collector._known_listing_visible = lambda *_args, **_kwargs: True  # type: ignore[method-assign]

        self.assertTrue(collector._return_to_parent("parent", "parent-id"))
        self.assertEqual(collector.current_path, "parent")
        self.assertEqual(collector.expected_id, "parent-id")

    def test_return_to_parent_uses_approved_folder_id_before_page_text(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.root_url = self.CGI_FOLDER_URL
        collector._wait_listing = lambda *_args, **_kwargs: True  # type: ignore[method-assign]
        collector._click_visible = lambda *_args, **_kwargs: self.fail("text navigation should not be needed")  # type: ignore[method-assign]

        self.assertTrue(collector._return_to_parent("parent", "parent-id"))
        self.assertIn("folderid=parent-id", collector.page.url)

    def test_return_to_parent_reboots_cached_folder_before_breadcrumb_fallback(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.root_url = self.CGI_FOLDER_URL
        collector._known_listing_visible = lambda *_args, **_kwargs: False  # type: ignore[method-assign]
        navigations: list[str] = []
        original_goto = collector.page.goto

        def capture_goto(url: str, **kwargs) -> None:
            navigations.append(url)
            original_goto(url, **kwargs)

        collector.page.goto = capture_goto  # type: ignore[method-assign]
        # First direct navigation has no XHR; the fresh document navigation does.
        calls = iter((False, True))
        collector._wait_listing = lambda *_args, **_kwargs: next(calls)  # type: ignore[method-assign]

        self.assertTrue(collector._return_to_parent("parent", "parent-id"))
        self.assertEqual(len(navigations), 2)
        self.assertIn("folderid=parent-id", navigations[-1])
        self.assertIn("weknora_inventory_nonce=", navigations[-1])

    def test_cached_parent_requires_matching_folder_id(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.expected_id = "parent-id"
        collector.children["parent"] = [("known", "child-id")]
        locator = type("VisibleLocator", (), {"count": lambda self: 1, "nth": lambda self, _index: self, "is_visible": lambda self: True})()
        collector.page.get_by_text = lambda *_args, **_kwargs: locator  # type: ignore[attr-defined, method-assign]

        self.assertFalse(collector._known_listing_visible("parent"))
        collector.page.url = self.CGI_FOLDER_URL.replace("folderid=s.1970325030004462.772095854VR0_d.7724", "folderid=parent-id")
        self.assertTrue(collector._known_listing_visible("parent"))

    def test_tree_walk_stops_unwinding_after_child_navigation_failure(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.children = {"": [("parent", "parent-id")], "parent": [("child", "child-id")]}
        collector._open_child = lambda _name, child_id: child_id == "parent-id"  # type: ignore[method-assign]
        collector._wait_listing = lambda *_args, **_kwargs: True  # type: ignore[method-assign]
        with patch("weknora_wedrive_agent.collector._load_virtualized_listing"), patch.object(collector, "_return_to_parent") as return_to_parent:
            collector._walk("", "root-id", 0, False, lambda _message: None)

        self.assertEqual(collector.errors, ["enter_click_failed"])
        return_to_parent.assert_not_called()

    def test_builds_child_route_from_current_folder_id(self) -> None:
        collector = self.collector(self.CGI_FOLDER_URL)
        collector.root_url = self.CGI_FOLDER_URL

        target = collector._folder_url_for("child-folder-id")

        self.assertIsNotNone(target)
        self.assertIn("folderid=child-folder-id", target or "")
        self.assertNotIn("folderid=s.1970325030004462.772095854VR0_d.7724", target or "")

    def test_captures_a_valid_share_link_from_browser_memory(self) -> None:
        page = BrowserCapturePage(self.HOME_URL)

        share_url = _browser_captured_share_url(page)  # type: ignore[arg-type]

        self.assertEqual(share_url, "https://drive.weixin.qq.com/s?k=share-token")

    def test_clicks_a_file_row_after_scrolling_the_virtualized_list(self) -> None:
        page = VirtualizedPage(self.CGI_FOLDER_URL)

        self.assertTrue(_click_virtualized_text(page, "off-screen.pdf", 1000, button="right"))  # type: ignore[arg-type]
        self.assertTrue(page.clicked)

    def test_reads_a_share_link_captured_inside_the_browser(self) -> None:
        page = VirtualizedPage(self.CGI_FOLDER_URL)

        self.assertEqual(_browser_captured_share_url(page), "https://drive.weixin.qq.com/s?k=captured-token")  # type: ignore[arg-type]

    def test_creates_share_links_for_all_thirteen_virtualized_file_rows(self) -> None:
        page = VirtualizedPage(self.CGI_FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)  # type: ignore[arg-type]
        collector.items = {
            f"file-{index}.md": {
                "name": f"file-{index}.md", "path": f"file-{index}.md", "item_type": "file",
                "share_url": "", "consumability": "supported",
            }
            for index in range(13)
        }
        with patch("weknora_wedrive_agent.collector._wait_for_share_toast_clear", return_value=True):
            collector._share_current_level("")

        self.assertTrue(page.clicked)
        self.assertEqual(collector.share_stats["created"], 13)
        self.assertEqual(collector.share_stats["failed"], 0)
        self.assertTrue(all(item["share_url"] for item in collector.items.values()))

    def test_launch_enables_the_chromium_sandbox(self) -> None:
        playwright = FakePlaywright()
        with patch("weknora_wedrive_agent.collector.sync_playwright", return_value=FakePlaywrightStarter(playwright)):
            runtime, _context = Collector.launch(self.profile_root)

        self.assertIs(runtime, playwright)
        self.assertTrue(playwright.chromium.kwargs["chromium_sandbox"])
        self.assertNotIn("--disable-blink-features=AutomationControlled", playwright.chromium.kwargs.get("args", []))

    def test_auto_share_keeps_the_link_captured_inside_the_wedrive_browser(self) -> None:
        # XHR responses do not reliably carry a newly-created share URL. The
        # Agent must persist the page-local copy result without reading the
        # user's Windows clipboard.
        page = SharePage(self.CGI_FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)  # type: ignore[arg-type]
        collector.items = {
            "proposal.pdf": {
                "name": "proposal.pdf",
                "item_type": "file",
                "share_url": "",
                "consumability": "supported",
            },
        }

        with patch("weknora_wedrive_agent.collector._click_last_visible_text", return_value=True), patch(
            "weknora_wedrive_agent.collector._browser_captured_share_url",
            return_value="https://drive.weixin.qq.com/s?k=share-token",
        ):
            collector._share_current_level("")

        self.assertEqual(collector.items["proposal.pdf"]["share_url"], "https://drive.weixin.qq.com/s?k=share-token")
        self.assertEqual(collector.share_stats["created"], 1)
        self.assertEqual(collector.share_stats["failed"], 0)

    def test_auto_share_records_a_safe_reason_when_row_is_not_available(self) -> None:
        page = SharePage(self.CGI_FOLDER_URL)
        collector = Collector(FakeContext(page), self.profile_root)  # type: ignore[arg-type]
        collector.items = {
            "proposal.pdf": {
                "name": "proposal.pdf",
                "item_type": "file",
                "share_url": "",
                "consumability": "supported",
            },
        }

        with patch("weknora_wedrive_agent.collector._wait_for_share_toast_clear", return_value=True), patch(
            "weknora_wedrive_agent.collector._click_last_visible_text", return_value=False
        ):
            collector._share_current_level("")

        self.assertEqual(collector.share_stats["failed"], 1)
        self.assertEqual(collector.items["proposal.pdf"]["failure_code"], "auto_share_row_unavailable")

    def test_follows_user_into_new_tab(self) -> None:
        # 登录后微盘可能新开标签页：构造时绑定的首页标签仍在首页，
        # 用户在新标签页进入了目标目录，应读取新标签页。
        home, folder = FakePage(self.HOME_URL), FakePage(self.CGI_FOLDER_URL)
        collector = Collector(FakeContext(home, folder), self.profile_root)  # type: ignore[arg-type]

        selected_url, _ = collector.selected_folder()

        self.assertEqual(selected_url, self.CGI_FOLDER_URL)

    def test_prefers_folder_tab_over_newer_home_tab(self) -> None:
        # 用户就在首个标签页进入了目录，但后来又开过一个微盘首页标签：
        # 仍应选中真正的文件夹页，而不是“最新”的首页。
        folder, home = FakePage(self.CGI_FOLDER_URL), FakePage(self.HOME_URL)
        collector = Collector(FakeContext(folder, home), self.profile_root)  # type: ignore[arg-type]

        selected_url, _ = collector.selected_folder()

        self.assertEqual(selected_url, self.CGI_FOLDER_URL)

    def test_home_page_only_still_rejected(self) -> None:
        with self.assertRaises(FolderSelectionError) as raised:
            self.collector(self.HOME_URL).selected_folder()
        self.assertEqual(raised.exception.code, "folder_not_selected")

    def test_space_home_explains_that_a_shared_space_is_not_a_folder(self) -> None:
        # This is the real URL shape captured from the Agent log. It is the
        # shared-space landing page, not the EVIP folder a user must enter.
        space_home = "https://drive.weixin.qq.com/webdisk/index?t=home#/space"

        with self.assertRaises(FolderSelectionError) as raised:
            self.collector(space_home).selected_folder()

        self.assertEqual(raised.exception.code, "space_home")
        self.assertIn("共享空间首页", str(raised.exception))

    def test_last_selected_root_is_restored_when_the_agent_opens_wecom(self) -> None:
        from tempfile import TemporaryDirectory

        with TemporaryDirectory() as directory:
            profile = Path(directory) / "profile"
            first_page = FakePage(self.CGI_FOLDER_URL)
            first = Collector(FakeContext(first_page), profile)  # type: ignore[arg-type]
            first.selected_folder()

            restarted_page = FakePage(self.HOME_URL)
            restarted = Collector(FakeContext(restarted_page), profile)  # type: ignore[arg-type]
            restarted.open_login()

            self.assertEqual(restarted_page.url, self.CGI_FOLDER_URL)

    def test_rejects_file_share_link_as_scan_root(self) -> None:
        with self.assertRaises(FolderSelectionError) as raised:
            self.collector("https://drive.weixin.qq.com/s?k=file-token").selected_folder()
        self.assertEqual(raised.exception.code, "folder_not_selected")

    def test_rejects_lookalike_host(self) -> None:
        with self.assertRaises(FolderSelectionError) as raised:
            self.collector("https://drive.weixin.qq.com.example.com/#/webdisk/folder").selected_folder()
        self.assertEqual(raised.exception.code, "not_wedrive")
