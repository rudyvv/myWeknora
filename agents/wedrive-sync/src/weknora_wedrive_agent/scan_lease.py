"""Keep one scan's lease alive without involving the browser thread."""
from __future__ import annotations

from datetime import datetime, timezone
import logging
import threading
import time

import httpx

from .client import APIClient, AgentUpgradeRequired, ScanAttemptConflict, ScanClaim


class ScanLease:
    RENEW_INTERVAL = 5 * 60
    RETRY_INTERVAL = 30
    NO_PROGRESS_TIMEOUT = 20 * 60
    MAX_DURATION = 4 * 60 * 60

    def __init__(self, api: APIClient, source_id: str, claim: ScanClaim):
        self.api, self.source_id, self.claim = api, source_id, claim
        self._lock = threading.RLock()
        self._stop = threading.Event()
        self._started = self._last_progress = time.monotonic()
        self._deadline = self._lease_deadline(claim)
        self._seq = 0
        self._error: RuntimeError | None = None
        self._thread: threading.Thread | None = None

    @staticmethod
    def _lease_deadline(claim: ScanClaim) -> float:
        return time.monotonic() + (claim.lease_expires_at - datetime.now(timezone.utc)).total_seconds()

    def __enter__(self) -> ScanLease:
        self.check()
        self._thread = threading.Thread(target=self._run, name="wedrive-scan-lease", daemon=True)
        self._thread.start()
        return self

    def __exit__(self, *_args) -> None:
        self.stop()

    def stop(self) -> None:
        self._stop.set()
        if self._thread is not None:
            # Renewal requests have a ten-second timeout; never leave a timer
            # sending renewals after this scan has completed or failed.
            self._thread.join(timeout=11)

    def check(self) -> None:
        with self._lock:
            now = time.monotonic()
            if self._error is None and (now >= self._deadline or now >= self._started + self.MAX_DURATION or now >= self._last_progress + self.NO_PROGRESS_TIMEOUT):
                self._error = ScanAttemptConflict()
            if self._error is not None:
                raise self._error

    def progress(self) -> None:
        # Browser response callbacks must not throw into Playwright's event
        # dispatch. The next explicit checkpoint propagates a lost lease.
        with self._lock:
            try:
                self.check()
            except RuntimeError:
                return
            if not self._stop.is_set():
                self._seq += 1
                self._last_progress = time.monotonic()

    def _run(self) -> None:
        delay = self.RENEW_INTERVAL
        while not self._stop.wait(delay):
            try:
                self.check()
                with self._lock:
                    seq = self._seq
                renewed = self.api.renew_scan_lease(self.source_id, self.claim.scan_attempt_id, seq)
                with self._lock:
                    self._deadline = self._lease_deadline(renewed)
                delay = self.RENEW_INTERVAL
            except (ScanAttemptConflict, AgentUpgradeRequired) as exc:
                with self._lock:
                    self._error = exc
                return
            except (httpx.TransportError, httpx.HTTPStatusError) as exc:
                if isinstance(exc, httpx.HTTPStatusError) and exc.response.status_code < 500 and exc.response.status_code != 429:
                    with self._lock:
                        self._error = ScanAttemptConflict()
                    return
                # Retry network/server outages only while the confirmed lease
                # is valid. Do not log HTTP exceptions containing URLs.
                logging.info("WeDrive lease retry source_id=%s error_type=%s", self.source_id, type(exc).__name__)
                delay = self.RETRY_INTERVAL
            except Exception:
                with self._lock:
                    self._error = ScanAttemptConflict()
                return
