from __future__ import annotations

import base64
import datetime as dt
import json
import logging
import mimetypes
import re
import time
from pathlib import Path
from urllib.parse import quote, urlsplit, urlunsplit

from playwright.sync_api import BrowserContext, Page, sync_playwright

WECOM_HOME = "https://drive.weixin.qq.com/#/"
CAPTURE_PATTERN = re.compile(r"file.?list|listfile|webdisk|diskspace", re.I)
SHARE_PATTERN = re.compile(r"^https://drive\.weixin\.qq\.com/s\?[^\s#]*\bk=[^\s&#]+(?:&[^\s#]*)?$")
SUPPORTED_OFFLINE = {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".rst", ".csv"}
SHARE_CAPTURE_KEY = "__weknoraWeDriveShareCapture"

# Keep a newly-created share link in the Agent page's JavaScript memory. The
# WeCom SPA normally copies that link to the system clipboard, but consuming
# that clipboard would race with the user's own copy/paste activity.
SHARE_CAPTURE_SCRIPT = f"""(() => {{
  const key = {SHARE_CAPTURE_KEY!r};
  const state = window[key] || {{ lastShareURL: '', installed: false }};
  if (state.installed) return;
  state.installed = true;
  window[key] = state;
  const capture = (value) => {{
    if (typeof value === 'string' && /^https:\/\/drive\.weixin\.qq\.com\/s\?/.test(value.trim())) {{
      state.lastShareURL = value.trim();
    }}
  }};
  document.addEventListener('copy', (event) => {{
    try {{ capture(event.clipboardData?.getData('text/plain') || window.getSelection()?.toString() || ''); }} catch (_) {{}}
  }}, true);
  try {{
    const clipboard = navigator.clipboard;
    if (clipboard && typeof clipboard.writeText === 'function') {{
      const writeText = clipboard.writeText.bind(clipboard);
      clipboard.writeText = async (value) => {{ capture(value); return writeText(value); }};
    }}
  }} catch (_) {{}}
  try {{
    const execCommand = document.execCommand.bind(document);
    document.execCommand = function(command, ...args) {{
      const selection = String(command).toLowerCase() === 'copy' ? window.getSelection()?.toString() || '' : '';
      const result = execCommand(command, ...args); capture(selection); return result;
    }};
  }} catch (_) {{}}
}})();"""


class FolderSelectionError(RuntimeError):
    """A user-actionable reason why the Agent cannot use the current tab."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class ScanFailure(RuntimeError):
    """A privacy-safe, diagnosable failure from the directory collector."""

    def __init__(self, code: str, message: str, diagnostics: dict[str, int] | None = None):
        super().__init__(message)
        self.code = code
        self.diagnostics = diagnostics or {}


def is_wecom_folder_url(value: str) -> bool:
    """Accept authenticated WeDrive folder pages, never file share links.

    The legacy web client uses ``#/webdisk/...``.  The current client uses
    ``/webdisk/index#/cgi/ssr/space/...`` and puts ``folderid`` after the
    fragment.  Both pages drive the same authenticated ``/webdisk/list`` XHR.
    """
    parsed = urlsplit(value)
    if parsed.scheme != "https" or parsed.hostname != "drive.weixin.qq.com":
        return False
    return (
        parsed.fragment.startswith("/webdisk/")
        or (
            parsed.path.rstrip("/") == "/webdisk/index"
            and parsed.fragment.startswith("/cgi/ssr/space/")
            and "folderid=" in parsed.fragment
        )
    )


def _url_shape(url: str) -> str:
    """Privacy-safe URL summary for diagnostics: structure only, no IDs."""
    try:
        parsed = urlsplit(url)
    except Exception:
        return "<unparseable>"
    parts = [f"{parsed.scheme}://{parsed.hostname or ''}{parsed.path}"]
    if parsed.query:
        keys = sorted(pair.split("=", 1)[0] for pair in parsed.query.split("&") if pair)
        parts.append("query[" + ",".join(keys) + "]")
    if parsed.fragment:
        segs = parsed.fragment.split("/")
        parts.append("frag=" + "/".join(segs[:4]) + ("/..." if len(segs) > 4 else ""))
        parts.append("folderid=" + ("yes" if "folderid=" in parsed.fragment else "no"))
    return " ".join(parts)


def _page_location(page: Page) -> str:
    """Return the URL currently exposed by the document, including SPA hashes.

    The current WeCom web disk is a single-page app.  On some Edge builds its
    hash navigation is visible in the address bar but ``Page.url`` still
    reports the original ``/webdisk/index?t=home`` navigation.  Reading the
    document location first keeps selecting a real folder deterministic; the
    property remains a fallback for pages that are loading or being closed.
    """
    try:
        value = page.evaluate("window.location.href")
        if isinstance(value, str) and value:
            return value
    except Exception:
        pass
    try:
        return page.url
    except Exception:
        return ""


def _inventory_navigation_url(folder_url: str) -> str:
    """Make a fresh document request while preserving the WeCom folder hash.

    WeCom's web disk is an SPA. Re-visiting or reloading an already-mounted
    route can restore the directory from in-memory state without issuing the
    list request that makes an inventory trustworthy. A nonce in the document
    query forces a new application boot; the ``folderid`` remains in the hash
    and is therefore unchanged.
    """
    parsed = urlsplit(folder_url)
    separator = "&" if parsed.query else ""
    query = f"{parsed.query}{separator}weknora_inventory_nonce={time.time_ns()}"
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, query, parsed.fragment))


def _deep_values(value, key_names: set[str]):
    if isinstance(value, dict):
        for key, item in value.items():
            if str(key).lower() in key_names:
                yield item
            yield from _deep_values(item, key_names)
    elif isinstance(value, list):
        for item in value:
            yield from _deep_values(item, key_names)


def _file_list(payload) -> list[dict]:
    """Return only the current directory's direct ``body.file_list``.

    A list response can contain nested metadata whose shape also resembles a
    file list. Recursively selecting the largest list can therefore make a
    metadata folder look like a direct child, and the next DFS navigation
    expects the wrong folder ID. The deepwiki-open RPA has the same proven
    restriction for this WeCom endpoint.
    """
    try:
        items = payload.get("body", {}).get("file_list", []) or []
    except AttributeError:
        return []
    return [item for item in items if isinstance(item, dict)]


def _deep_find_listing_id(value) -> str:
    """Find a folder ID in the request payload when a list is empty."""
    if isinstance(value, dict):
        for key, item in value.items():
            if str(key).lower().replace("_", "") in {"fatherid", "folderid"} and item:
                return str(item)
        for item in value.values():
            found = _deep_find_listing_id(item)
            if found:
                return found
    elif isinstance(value, list):
        for item in value:
            found = _deep_find_listing_id(item)
            if found:
                return found
    return ""


def _folder_breadcrumb(page: Page) -> str:
    """Read the visible WeDrive breadcrumb for human confirmation only.

    Inventory remains protocol-backed; this is deliberately not used as a
    navigation authority.  It prevents a generic browser title such as
    ``企业微信·微盘`` from making an unrelated folder look selected in the
    setup page.
    """
    script = r"""() => {
      const clean = (value) => String(value || '').replace(/\s+/g, ' ').trim();
      const selectors = [
        '.fileBreadcrumb_core', '.fileBreadcrumb_breadcrumb',
        '[class*="breadcrumb"]', '[class*="Breadcrumb"]',
        '[class*="crumb"]', '[class*="Crumb"]',
        '[aria-label*="breadcrumb"]', '[aria-label*="面包屑"]'
      ];
      const containers = [];
      for (const selector of selectors) {
        try { containers.push(...Array.from(document.querySelectorAll(selector))); } catch (_) {}
      }
      const values = [];
      for (const container of containers) {
        const titled = Array.from(container.querySelectorAll('[title]'))
          .map((node) => clean(node.getAttribute('title'))).filter(Boolean);
        const parts = titled.length ? titled : String(container.innerText || '')
          .split(/[›>\n]/).map(clean).filter((part) => part && !/^\.{2,}$/.test(part));
        if (parts.length >= 2) values.push(parts.join(' › '));
      }
      return values.sort((left, right) => right.length - left.length)[0] || '';
    }"""
    try:
        value = page.evaluate(script)
    except Exception:
        return ""
    return str(value).strip() if isinstance(value, str) else ""


def _listing_id(payload, response) -> str:
    """Return the remote folder ID represented by this list response."""
    father_ids = {str(item.get("father_id")) for item in _file_list(payload) if item.get("father_id")}
    if len(father_ids) == 1:
        return next(iter(father_ids))
    try:
        request_payload = response.request.post_data_json
    except Exception:
        request_payload = None
    if request_payload is None:
        try:
            raw = response.request.post_data
            request_payload = json.loads(raw) if raw else None
        except Exception:
            request_payload = None
    return _deep_find_listing_id(request_payload)


def _first(mapping: dict, *keys, default=""):
    lower = {str(key).lower(): value for key, value in mapping.items()}
    return next((lower[key] for key in keys if key in lower and lower[key] not in (None, "")), default)


def _share_url(item: dict) -> str:
    direct = str(_first(item, "share_url", "shareurl", "share_uri", "share_link"))
    if direct and SHARE_PATTERN.match(direct):
        return direct
    code = str(_first(item, "share_code"))
    return f"https://drive.weixin.qq.com/s?k={code}" if code else ""


def _valid_share_url(value: object) -> str:
    value = str(value).strip() if value else ""
    return value if SHARE_PATTERN.match(value) else ""


def _install_share_capture(page: Page) -> None:
    """Install the capture in the currently loaded document too."""
    try:
        page.evaluate(SHARE_CAPTURE_SCRIPT)
    except Exception:
        pass


def _clear_browser_share_capture(page: Page) -> None:
    try:
        page.evaluate("(key) => { const state = window[key]; if (state) state.lastShareURL = ''; }", SHARE_CAPTURE_KEY)
    except Exception:
        pass


def _browser_captured_share_url(page: Page) -> str:
    """Return only the share URL captured in this browser document."""
    try:
        value = page.evaluate("(key) => window[key]?.lastShareURL || ''", SHARE_CAPTURE_KEY)
    except Exception:
        value = ""
    return _valid_share_url(value)


def _click_last_visible_text(page: Page, text: str, timeout_ms: int, button: str = "left", force: bool = True) -> bool:
    """Wait for a SPA menu entry and select the newest visible instance."""
    deadline = time.monotonic() + timeout_ms / 1000
    while True:
        for builder in (
            lambda: page.get_by_role("menuitem", name=text, exact=True),
            lambda: page.get_by_text(text, exact=True),
            lambda: page.get_by_text(text, exact=False),
        ):
            try:
                locator = builder()
                for index in range(locator.count() - 1, -1, -1):
                    candidate = locator.nth(index)
                    if candidate.is_visible():
                        candidate.click(button=button, force=force, timeout=min(timeout_ms, 1500))
                        return True
            except Exception:
                continue
        if time.monotonic() >= deadline:
            return False


def _scroll_virtualized_list(page: Page, reset: bool = False) -> dict[str, bool]:
    """Advance the active virtualized list without relying on a fixed class name."""
    value = page.evaluate(
        """({ reset }) => {
            const candidates = Array.from(document.querySelectorAll('*')).filter((node) => {
                const style = getComputedStyle(node);
                return node.scrollHeight > node.clientHeight + 8 &&
                    /(auto|scroll)/.test(style.overflowY || '');
            });
            const target = candidates.sort((a, b) =>
                (b.scrollHeight - b.clientHeight) - (a.scrollHeight - a.clientHeight)
            )[0] || document.scrollingElement;
            if (!target) return { moved: false, at_end: true };
            if (reset) target.scrollTop = 0;
            const before = target.scrollTop;
            target.scrollBy(0, Math.max(240, Math.floor(target.clientHeight * 0.8)));
            return {
                moved: target.scrollTop !== before,
                at_end: target.scrollTop + target.clientHeight >= target.scrollHeight - 2,
            };
        }""",
        {"reset": reset},
    )
    return {
        "moved": bool(value.get("moved")) if isinstance(value, dict) else False,
        "at_end": bool(value.get("at_end")) if isinstance(value, dict) else True,
    }


def _click_virtualized_text(page: Page, text: str, timeout_ms: int, button: str = "left", force: bool = True) -> bool:
    """Click a text row, scrolling a virtualized WeCom list when necessary."""
    if _click_last_visible_text(page, text, min(timeout_ms, 1500), button, force):
        return True
    deadline = time.monotonic() + timeout_ms / 1000
    reset = True
    while time.monotonic() < deadline:
        try:
            state = _scroll_virtualized_list(page, reset)
        except Exception:
            return False
        reset = False
        page.wait_for_timeout(180)
        if _click_last_visible_text(page, text, min(1200, max(100, int((deadline - time.monotonic()) * 1000)),), button, force):
            return True
        if not state["moved"] or state["at_end"]:
            return False
    return False


def _load_virtualized_listing(page: Page, timeout_ms: int = 8000) -> None:
    """Force WeCom's virtual list to request all currently reachable pages."""
    deadline = time.monotonic() + timeout_ms / 1000
    reset = True
    while time.monotonic() < deadline:
        try:
            state = _scroll_virtualized_list(page, reset)
        except Exception:
            return
        reset = False
        page.wait_for_timeout(220)
        if not state["moved"] or state["at_end"]:
            return


def _dismiss_share_toast(page: Page) -> bool:
    """Remove only WeCom's local copied-link toast after the URL is captured."""
    try:
        removed = page.evaluate(
            """(message) => {
                const leaves = Array.from(document.querySelectorAll('*')).filter(
                    node => node.children.length === 0 && node.textContent.trim() === message
                );
                let count = 0;
                for (const leaf of leaves) {
                    let candidate = leaf;
                    for (let parent = leaf.parentElement, depth = 0;
                         parent && depth < 8; parent = parent.parentElement, depth++) {
                        const rect = parent.getBoundingClientRect();
                        if (rect.width >= 180 && rect.width <= 700 && rect.height >= 40 && rect.height <= 300) {
                            candidate = parent;
                        }
                        if (rect.width > 700 || rect.height > 300) break;
                    }
                    candidate.remove(); count++;
                }
                return count;
            }""",
            "已复制分享链接",
        )
        return bool(removed)
    except Exception:
        return False


def _wait_for_share_toast_clear(page: Page, timeout_ms: int = 7000) -> bool:
    """Avoid a copied-link toast intercepting the next file-row right click."""
    deadline = time.monotonic() + timeout_ms / 1000
    while True:
        try:
            toast = page.get_by_text("已复制分享链接", exact=False)
            visible = any(toast.nth(index).is_visible() for index in range(toast.count()))
        except Exception:
            visible = False
        if not visible:
            return True
        if _dismiss_share_toast(page):
            continue
        if time.monotonic() >= deadline:
            return False
        try:
            page.wait_for_timeout(100)
        except Exception:
            return False
        try:
            page.wait_for_timeout(100)
        except Exception:
            return False


class Collector:
    """XHR-first DFS collector adapted from deepwiki-open's proven RPA.

    Directory navigation is accepted only after `/webdisk/list` reports the
    expected father/listing ID. Any ambiguity marks the snapshot incomplete,
    so the server never interprets a partial scan as deletion.
    """

    def __init__(self, context: BrowserContext, profile_root: Path):
        self.context = context
        self.page = context.pages[0] if context.pages else context.new_page()
        _install_share_capture(self.page)
        self.profile_root = profile_root
        # This is local-only routing metadata, not a share link or login
        # credential. Keeping it next to the Agent profile lets the headed
        # browser return to the user's approved candidate after a restart.
        self.last_root_path = profile_root.parent / "last-root.json"
        self.items: dict[str, dict] = {}
        self.children: dict[str, list[tuple[str, str]]] = {}
        self.current_path = ""
        self.root_url = ""
        self.current_parent_id = ""
        self.expected_id = ""
        self.accepted_seq = 0
        self.errors: list[str] = []
        self.share_target = ""
        self.share_stats = {"existing": 0, "created": 0, "failed": 0}
        self.diagnostics = self._new_diagnostics()
        self._bound_page: Page | None = None
        self._inventory_progress = lambda: None
        self._checkpoint = lambda: None
        self._progress_listings: set[tuple] = set()
        self._progress_items: set[str] = set()
        self._progress_shares: set[str] = set()
        self._bind_page(self.page)

    @staticmethod
    def _new_diagnostics() -> dict[str, int]:
        # Counts only protocol categories. Do not put URLs, titles, filenames,
        # IDs, cookies, or share links here: this object is written to logs.
        return {
            "candidate_responses": 0,
            "json_responses": 0,
            "list_responses": 0,
            "accepted_listings": 0,
            "enter_click_failed": 0,
            "enter_listing_timeout": 0,
            "return_navigation_failed": 0,
            "return_cache_verified": 0,
            "return_direct_attempts": 0,
            "return_fresh_attempts": 0,
            "direct_folder_navigation": 0,
            "direct_navigation_failed": 0,
        }

    @staticmethod
    def launch(profile_root: Path):
        playwright = sync_playwright().start()
        profile_root.mkdir(parents=True, exist_ok=True)
        context = None
        failures = []
        # Reuse the managed browser already installed on Windows. The packaged
        # Agent therefore does not need to ship a second Chromium runtime.
        for channel in ("msedge", "chrome"):
            try:
                context = playwright.chromium.launch_persistent_context(
                    str(profile_root), channel=channel, headless=False, chromium_sandbox=True,
                )
                break
            except Exception as exc:
                failures.append(f"{channel}: {exc}")
        if context is None:
            playwright.stop()
            raise RuntimeError("无法启动 Microsoft Edge 或 Google Chrome，请先安装其中一个浏览器") from RuntimeError("; ".join(failures))
        context.add_init_script("Object.defineProperty(navigator,'webdriver',{get:()=>undefined})")
        context.add_init_script(SHARE_CAPTURE_SCRIPT)
        return playwright, context

    def logged_in(self) -> bool:
        try:
            return "/webdisk/" in _page_location(self.page) or len(self.page.inner_text("body").strip()) > 300
        except Exception:
            return False

    def is_alive(self) -> bool:
        """Whether the user has kept at least one Agent-controlled tab open."""
        try:
            return any(not page.is_closed() for page in self.context.pages)
        except Exception:
            return False

    def _load_last_root_url(self) -> str:
        try:
            value = json.loads(self.last_root_path.read_text(encoding="utf-8")).get("root_url", "")
            return value if isinstance(value, str) and is_wecom_folder_url(value) else ""
        except (OSError, ValueError, TypeError, AttributeError):
            return ""

    def _remember_root_url(self, value: str) -> None:
        if not is_wecom_folder_url(value):
            return
        try:
            self.last_root_path.parent.mkdir(parents=True, exist_ok=True)
            self.last_root_path.write_text(json.dumps({"root_url": value}), encoding="utf-8")
        except OSError:
            logging.warning("Unable to persist the last WeDrive root URL")

    def open_login(self) -> bool:
        """Open WeDrive, preferring the last root selected on this device.

        Returns whether a directory was restored so the caller can give the
        user a precise instruction rather than sending them to the space home.
        """
        self.page = self._resolve_page(prefer_folder=False)
        restored = self._load_last_root_url()
        target = restored or WECOM_HOME
        self.page.bring_to_front()
        try:
            self.page.goto(target, wait_until="domcontentloaded")
            if restored:
                logging.info("Opened the last selected WeDrive root")
            return bool(restored)
        except Exception:
            if not restored:
                raise
            logging.warning("Unable to reopen the last WeDrive root; using the WeDrive home page")
            self.page.goto(WECOM_HOME, wait_until="domcontentloaded")
            return False

    def _bind_page(self, page: Page) -> Page:
        """Attach the XHR capture listener to ``page`` exactly once."""
        _install_share_capture(page)
        if page is not self._bound_page:
            page.on("response", self._on_response)
            self._bound_page = page
        return page

    def _resolve_page(self, prefer_folder: bool = True) -> Page:
        """Return the tab the user is actually working in.

        ``self.page`` is captured once at construction. Login redirects and
        WeDrive itself may open new tabs (or the user may close the original
        one), leaving ``self.page`` pointing at a stale home/login tab while
        the user navigates elsewhere. Scan live pages instead: prefer the
        newest tab showing a WeDrive folder, then any WeDrive tab, then the
        newest tab overall.
        """
        try:
            pages = [page for page in self.context.pages if not page.is_closed()]
        except Exception:
            pages = []
        if not pages:
            return self.page
        if prefer_folder:
            for page in reversed(pages):
                try:
                    if is_wecom_folder_url(_page_location(page)):
                        self.page = self._bind_page(page)
                        return page
                except Exception:
                    continue
        for page in reversed(pages):
            try:
                if urlsplit(_page_location(page)).hostname == "drive.weixin.qq.com":
                    self.page = self._bind_page(page)
                    return page
            except Exception:
                continue
        self.page = self._bind_page(pages[-1])
        return self.page

    def selected_folder(self) -> tuple[str, str]:
        page = self._resolve_page()
        value = _page_location(page)
        if not is_wecom_folder_url(value):
            shapes = []
            try:
                for candidate in self.context.pages:
                    try:
                        shapes.append(_url_shape(_page_location(candidate)))
                    except Exception:
                        shapes.append("<unreadable>")
            except Exception:
                shapes.append("<context-unreadable>")
            logging.warning(
                "select_folder: chosen page is not a WeDrive folder; chosen=%s live_pages=%s",
                _url_shape(value),
                shapes,
            )
            parsed = urlsplit(value)
            if parsed.hostname == "drive.weixin.qq.com" and parsed.fragment == "/space":
                raise FolderSelectionError(
                    "space_home",
                    "Agent 当前停留在企业微信微盘的共享空间首页；请在这个 Agent 打开的浏览器窗口中点击进入 EVIP 目标文件夹后再读取。",
                )
            if parsed.hostname == "drive.weixin.qq.com":
                raise FolderSelectionError(
                    "folder_not_selected",
                    "Agent 已打开企业微信微盘，但当前不是可同步的文件夹页；请在 Agent 浏览器中进入目标文件夹后再读取。",
                )
            raise FolderSelectionError(
                "not_wedrive",
                "Agent 当前浏览器不是企业微信微盘页面；请点击“登录企业微信”打开 Agent 微盘浏览器后再进入目标文件夹。",
            )
        logging.info("select_folder matched: %s", _url_shape(value))
        self._remember_root_url(value)
        # The folder URL updates before WeCom finishes rendering its
        # breadcrumb.  Wait briefly so the setup UI can show the actual path
        # rather than its generic page title.
        breadcrumb = ""
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            breadcrumb = _folder_breadcrumb(page)
            if breadcrumb:
                break
            try:
                page.wait_for_timeout(150)
            except Exception:
                break
        # The browser title is normally the fixed product name, not a folder
        # identity.  Use it only when a breadcrumb is unavailable.
        return value, (breadcrumb or page.title() or "企业微信微盘目录").strip()

    def qr_data_uri(self) -> str:
        candidates = self.page.locator("canvas, img").all()
        for candidate in candidates:
            try:
                box = candidate.bounding_box()
                if box and 120 <= box["width"] <= 420 and 120 <= box["height"] <= 420 and candidate.is_visible():
                    raw = candidate.screenshot(type="png")
                    return "data:image/png;base64," + base64.b64encode(raw).decode()
            except Exception:
                pass
        return ""

    def _on_response(self, response) -> None:
        if not CAPTURE_PATTERN.search(response.url):
            return
        self.diagnostics["candidate_responses"] += 1
        if response.status != 200:
            return
        try:
            payload = response.json()
        except Exception:
            return
        self.diagnostics["json_responses"] += 1
        if self.share_target:
            urls = [str(value) for value in _deep_values(payload, {"share_url", "shareurl", "share_uri", "share_link"})]
            codes = [str(value) for value in _deep_values(payload, {"share_code"})]
            urls += [f"https://drive.weixin.qq.com/s?k={code}" for code in codes if code]
            valid = list(dict.fromkeys(url for url in urls if SHARE_PATTERN.match(url)))
            if len(valid) == 1 and self.share_target in self.items:
                self.items[self.share_target]["share_url"] = valid[0]
        if "/webdisk/list" not in response.url:
            return
        self.diagnostics["list_responses"] += 1
        parent_id = _listing_id(payload, response)
        if self.expected_id and parent_id != self.expected_id:
            return
        if not isinstance(payload, dict) or payload.get("errcode", 0) not in (0, "0"):
            return
        body = payload.get("body")
        if not isinstance(body, dict) or not isinstance(body.get("file_list"), list):
            return
        if any(str(raw.get("father_id") or parent_id) != parent_id for raw in _file_list(payload)):
            return
        listing = (parent_id, tuple(sorted(str(_first(raw, "file_id", "fileid", "doc_id", "docid")) for raw in _file_list(payload))))
        if listing not in self._progress_listings:
            self._progress_listings.add(listing)
            self._inventory_progress()
        self.current_parent_id = parent_id
        self.accepted_seq += 1
        self.diagnostics["accepted_listings"] += 1
        # A virtualized folder can issue more than one list response while we
        # scroll. Merge pages instead of discarding siblings from earlier
        # responses; repeated complete responses remain idempotent.
        children = list(self.children.get(self.current_path, []))
        child_ids = {child_id for _, child_id in children}
        for raw in _file_list(payload):
            name = str(_first(raw, "name", "file_name", "filename", "title")).strip()
            file_id = str(_first(raw, "file_id", "fileid"))
            doc_id = str(_first(raw, "doc_id", "docid"))
            if not name or not (file_id or doc_id):
                continue
            external_id = file_id or doc_id
            parent_external_id = parent_id
            path = f"{self.current_path}/{name}".strip("/")
            file_type = _first(raw, "file_type", "filetype", "type")
            is_dir = file_type == 1 or str(file_type).lower() in {"dir", "folder"} or bool(_first(raw, "is_dir", "isdir", default=False))
            modified = _first(raw, "mtime", "modified_at")
            modified_at = None
            if isinstance(modified, (int, float)) and modified > 0:
                modified_at = dt.datetime.fromtimestamp(modified, dt.timezone.utc).isoformat().replace("+00:00", "Z")
            item = {
                "external_id": external_id, "parent_external_id": parent_external_id,
                "name": name, "path": path, "item_type": "folder" if is_dir else ("online_document" if doc_id else "file"),
                "doc_id": doc_id, "share_url": _share_url(raw),
                "mime_type": mimetypes.guess_type(name)[0] or "", "size": int(_first(raw, "size", "file_size", default=0) or 0),
                "modified_at": modified_at, "content_fingerprint": str(_first(raw, "etag", "version", "sha1")),
                "consumability": "supported" if (is_dir or doc_id or Path(name).suffix.lower() in SUPPORTED_OFFLINE) else "unsupported",
            }
            self.items[path] = item
            if external_id not in self._progress_items:
                self._progress_items.add(external_id)
                self._inventory_progress()
            if item["share_url"]:
                self.share_stats["existing"] += 1
            if is_dir and external_id not in child_ids:
                children.append((name, external_id))
                child_ids.add(external_id)
        self.children[self.current_path] = children

    def collect(self, root_url: str, auto_share: bool, progress=lambda _: None, *, on_progress=lambda: None, checkpoint=lambda: None) -> tuple[str, list[dict], dict]:
        self._inventory_progress, self._checkpoint = on_progress, checkpoint
        self._progress_listings.clear()
        self._progress_items.clear()
        self._progress_shares.clear()
        try:
            return self._collect(root_url, auto_share, progress)
        finally:
            self._inventory_progress, self._checkpoint = lambda: None, lambda: None

    def _collect(self, root_url: str, auto_share: bool, progress) -> tuple[str, list[dict], dict]:
        self._checkpoint()
        if not is_wecom_folder_url(root_url):
            raise ScanFailure("invalid_root", "请在 Agent 浏览器中进入目标微盘文件夹后重新读取")
        self.items.clear(); self.children.clear(); self.errors.clear()
        self.share_stats = {"existing": 0, "created": 0, "failed": 0}
        self.diagnostics = self._new_diagnostics()
        self.current_path = ""
        self.root_url = root_url
        self.current_parent_id = ""
        # A Collector instance serves every approved source on this device.
        # The first response can arrive late from the previous folder. Accept
        # only the approved root ID from this source's URL, just as child
        # navigation accepts only the expected child ID.
        root_id = self._id_from_url(root_url)
        if not root_id:
            raise ScanFailure("missing_root_id", "无法从微盘目录链接确定同步根目录 ID", dict(self.diagnostics))
        self.expected_id = root_id
        marker = self.accepted_seq
        self.page = self._resolve_page()
        # Always enter the root through a fresh application boot. The page the
        # user used to select this folder is often already mounted, and WeCom
        # otherwise returns a cached DOM without its list protocol response.
        self.page.goto(_inventory_navigation_url(root_url), wait_until="domcontentloaded")
        if not self._wait_listing(15, marker):
            self._checkpoint()
            # One controlled retry covers transient application boot failures.
            # Use another nonce rather than ``reload``: an SPA reload can still
            # reuse the page's in-memory directory payload.
            try:
                self.page.goto(_inventory_navigation_url(root_url), wait_until="domcontentloaded")
            except Exception:
                pass
            if not self._wait_listing(15, marker):
                raise ScanFailure("listing_timeout", "未捕获到微盘目录列表响应，请确认登录状态和目录链接", dict(self.diagnostics))
        _load_virtualized_listing(self.page)
        root_name = self.page.title() or "WeDrive Root"
        self.items["."] = {"external_id": root_id, "parent_external_id": "", "name": root_name, "path": ".", "item_type": "folder", "doc_id": "", "share_url": "", "mime_type": "", "size": 0, "content_fingerprint": "", "consumability": "supported"}
        self._inventory_progress()
        self._walk("", root_id, 0, auto_share, progress)
        if self.errors:
            raise ScanFailure("tree_walk_failed", "目录树遍历未完成", dict(self.diagnostics))
        return root_id, sorted(self.items.values(), key=lambda item: item["path"]), dict(self.share_stats)

    def _walk(self, logical_path: str, parent_id: str, depth: int, auto_share: bool, progress) -> None:
        self._checkpoint()
        if depth > 64:
            raise RuntimeError("目录深度超过安全上限 64")
        if auto_share:
            self._share_current_level(logical_path)
        for name, child_id in list(self.children.get(logical_path, [])):
            self._checkpoint()
            progress(f"正在扫描 {logical_path}/{name}".strip("/"))
            seq = self.accepted_seq
            self.current_path = f"{logical_path}/{name}".strip("/")
            self.expected_id = child_id
            if not self._open_child(name, child_id):
                self.diagnostics["enter_click_failed"] += 1
                self.errors.append("enter_click_failed")
                return
            if not self._wait_listing(12, seq):
                self.diagnostics["enter_listing_timeout"] += 1
                self.errors.append("enter_listing_timeout")
                return
            _load_virtualized_listing(self.page)
            self._walk(self.current_path, child_id, depth + 1, auto_share, progress)
            if self.errors:
                return
            if not self._return_to_parent(logical_path, parent_id):
                self.diagnostics["return_navigation_failed"] += 1
                self.errors.append("return_navigation_failed")
                return

    def _folder_url_for(self, folder_id: str) -> str | None:
        """Build a child-folder route from the approved root URL.

        The current WeCom SPA encodes the selected folder in the fragment
        query.  We only replace that value after receiving the child ID in a
        validated ``/webdisk/list`` response; no title or hand-entered path is
        used as a navigation authority.
        """
        if not folder_id:
            return None
        try:
            parsed = urlsplit(self.root_url)
        except ValueError:
            return None

        def replace(value: str) -> tuple[str, bool]:
            replaced = False

            def repl(match) -> str:
                nonlocal replaced
                replaced = True
                return match.group(1) + quote(folder_id, safe="._-")

            return re.sub(r"([?&](?:folderid|fileid|id)=)[^&#]*", repl, value, count=1, flags=re.I), replaced

        fragment, replaced = replace(parsed.fragment)
        query = parsed.query
        if not replaced:
            query, replaced = replace(query)
        if not replaced:
            return None
        return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, query, fragment))

    def _open_child(self, name: str, child_id: str) -> bool:
        """Open a known child by ID before falling back to a visible label."""
        target = self._folder_url_for(child_id)
        if target:
            try:
                self.page.goto(target, wait_until="domcontentloaded")
                self.diagnostics["direct_folder_navigation"] += 1
                return True
            except Exception:
                self.diagnostics["direct_navigation_failed"] += 1
        self._checkpoint()
        return self._click_visible(name)

    def _known_listing_visible(self, logical_path: str) -> bool:
        """Validate a cached parent view without accepting DOM as inventory.

        Returning through a WeCom breadcrumb can restore the parent from SPA
        cache without a fresh ``/webdisk/list`` request.  The directory was
        already captured and ID-validated before descent; checking two known
        direct labels only verifies navigation, never creates inventory data.
        """
        if self._id_from_url(_page_location(self.page)) != self.expected_id:
            return False
        labels = [name for name, _ in self.children.get(logical_path, [])][:3]
        prefix = f"{logical_path}/" if logical_path else ""
        labels.extend(
            item["name"]
            for path, item in self.items.items()
            if path != "." and path.startswith(prefix) and "/" not in path[len(prefix):]
        )
        labels = list(dict.fromkeys(labels))[:3]
        if not labels:
            return False
        visible = 0
        for label in labels:
            try:
                locator = self.page.get_by_text(label, exact=True)
                if any(locator.nth(index).is_visible() for index in range(locator.count())):
                    visible += 1
            except Exception:
                continue
        return visible >= min(2, len(labels))

    def _return_to_parent(self, logical_path: str, parent_id: str) -> bool:
        """Return to a verified parent, tolerating a WeCom SPA cache restore."""
        self.current_path = logical_path
        self.expected_id = parent_id
        target = self._folder_url_for(parent_id)
        if target:
            # The parent ID came from an accepted listing. Navigating by ID
            # avoids ambiguous breadcrumb labels and browser history altered
            # by share-link UI actions. A cached SPA page may emit no XHR, so
            # retry with a fresh document before falling back to the UI.
            for url, timeout, counter in (
                (target, 2, "return_direct_attempts"),
                (_inventory_navigation_url(target), 12, "return_fresh_attempts"),
            ):
                self._checkpoint()
                self.diagnostics[counter] += 1
                marker = self.accepted_seq
                try:
                    self.page.goto(url, wait_until="domcontentloaded")
                    if self._wait_listing(timeout, marker):
                        return True
                    if self._known_listing_visible(logical_path):
                        self.diagnostics["return_cache_verified"] += 1
                        return True
                except Exception:
                    pass

        # Keep the original UI fallback for older WeCom routes that cannot
        # encode the validated parent ID directly.
        self._checkpoint()
        marker = self.accepted_seq
        parent_label = logical_path.rsplit("/", 1)[-1] if logical_path else ""
        try:
            if parent_label:
                navigated = self._click_visible(parent_label)
            else:
                self.page.goto(self.root_url, wait_until="domcontentloaded")
                navigated = True
        except Exception:
            navigated = False
        if navigated:
            if self._wait_listing(12, marker):
                return True
            if self._known_listing_visible(logical_path):
                self.diagnostics["return_cache_verified"] += 1
                return True

        # Some layouts collapse a parent breadcrumb. Browser history is a
        # last-resort only and must be validated by the same two safe signals.
        self._checkpoint()
        marker = self.accepted_seq
        try:
            self.page.go_back(wait_until="networkidle")
        except Exception:
            pass
        if self._wait_listing(8, marker):
            return True
        if self._known_listing_visible(logical_path):
            self.diagnostics["return_cache_verified"] += 1
            return True
        return False

    def _wait_listing(self, seconds: int, after: int | None = None) -> bool:
        marker = self.accepted_seq if after is None else after
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            self._checkpoint()
            if self.accepted_seq > marker:
                return True
            self.page.wait_for_timeout(100)
        return False

    def _click_visible(self, text: str, button="left") -> bool:
        locator = self.page.get_by_text(text, exact=True)
        for index in range(locator.count()):
            try:
                if locator.nth(index).is_visible():
                    locator.nth(index).click(button=button, timeout=2500)
                    return True
            except Exception:
                pass
        return False

    def _share_current_level(self, logical_path: str) -> None:
        prefix = f"{logical_path}/" if logical_path else ""
        attempted: set[str] = set()
        while True:
            # Scrolling can trigger another virtual-list page response. Keep
            # picking up new direct children until every discovered file has
            # been handled exactly once.
            targets = [
                path for path, item in self.items.items()
                if path not in attempted and item["item_type"] == "file" and not item["share_url"]
                and item["consumability"] == "supported" and path.startswith(prefix)
                and "/" not in path[len(prefix):]
            ]
            if not targets:
                return
            for path in targets:
                self._checkpoint()
                attempted.add(path)
                self.share_target = path
                name = self.items[path]["name"]
                try:
                    if not _wait_for_share_toast_clear(self.page):
                        self._record_share_failure(path, "ui_busy")
                        continue
                    if not _click_virtualized_text(self.page, name, 6500, "right", force=False):
                        self._record_share_failure(path, "row_unavailable")
                        continue

                    _clear_browser_share_capture(self.page)
                    opened = False
                    for label in ("获取分享链接", "分享", "分享文件"):
                        if _click_last_visible_text(self.page, label, 2200):
                            opened = True
                            break
                    if not opened:
                        self._record_share_failure(path, "action_unavailable")
                        continue

                    # Capture WeCom's copy operation inside its own browser
                    # document. Never read or paste the user's Windows
                    # clipboard, so their concurrent copy/paste cannot become
                    # another file's share link.
                    self.page.wait_for_timeout(900)
                    share_url = self.items[path]["share_url"] or _browser_captured_share_url(self.page)
                    if not share_url:
                        for label in ("开启链接分享", "创建链接", "创建分享链接", "开启分享", "复制链接"):
                            if _click_last_visible_text(self.page, label, 1500):
                                self.page.wait_for_timeout(1800)
                                share_url = self.items[path]["share_url"] or _browser_captured_share_url(self.page)
                                if share_url:
                                    break
                    if share_url:
                        self.items[path]["share_url"] = share_url
                        self.share_stats["created"] += 1
                    else:
                        self._record_share_failure(path, "link_not_created")
                except Exception:
                    # A single file that cannot expose a link is a consumability
                    # failure, not a reason to throw away a complete directory
                    # inventory. The server will mark it as partial on download.
                    self._record_share_failure(path, "unexpected")
                finally:
                    try:
                        self.page.keyboard.press("Escape")
                        self.page.wait_for_timeout(500)
                        _wait_for_share_toast_clear(self.page)
                    except Exception:
                        pass
                    self.share_target = ""
                    if path not in self._progress_shares:
                        self._progress_shares.add(path)
                        self._inventory_progress()

    def _record_share_failure(self, path: str, reason: str) -> None:
        """Keep a safe per-entry reason for the server's aggregate report.

        No file name, path, UI text, clipboard content, or browser exception
        leaves this method. The server stores and presents only reason counts.
        """
        self.share_stats["failed"] += 1
        item = self.items.get(path)
        if item is not None:
            item["failure_code"] = f"auto_share_{reason}"

    @staticmethod
    def _id_from_url(value: str) -> str:
        parsed = urlsplit(value)
        match = re.search(r"(?:fileid|folderid|id)=([^&]+)", parsed.query + "&" + parsed.fragment, re.I)
        return match.group(1) if match else ""
