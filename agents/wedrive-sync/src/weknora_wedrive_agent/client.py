from __future__ import annotations

import json
import time
from urllib.parse import urlsplit

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from . import __version__
from .security import Identity


class APIClient:
    def __init__(self, identity: Identity):
        self.identity = identity
        self.http = httpx.Client(base_url=identity.server, timeout=60)

    @staticmethod
    def register(server: str, code: str, name: str) -> tuple[dict, Ed25519PrivateKey]:
        key = Ed25519PrivateKey.generate()
        response = httpx.post(server.rstrip("/") + "/api/v1/wedrive/agent/register", json={
            "code": code, "name": name, "public_key": Identity.public_key(key),
            "agent_version": __version__, "platform": "windows",
        }, timeout=30)
        response.raise_for_status()
        return response.json(), key

    def _request(self, method: str, path: str, payload: dict | None = None, *, allow_statuses: tuple[int, ...] = ()) -> httpx.Response:
        body = b"" if payload is None else json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
        timestamp = str(int(time.time()))
        headers = {
            "X-WeDrive-Device-ID": self.identity.device_id,
            "X-WeDrive-Timestamp": timestamp,
            "X-WeDrive-Signature": self.identity.sign(method, path, timestamp, body),
            "X-WeDrive-Agent-Version": __version__,
            "Content-Type": "application/json",
        }
        response = self.http.request(method, path, content=body, headers=headers)
        if response.status_code not in allow_statuses:
            response.raise_for_status()
        return response

    def list_sources(self) -> list[dict]:
        return self._request("GET", "/api/v1/wedrive/agent/sources").json()

    def claim_scan(self, source_id: str, trigger: str) -> bool:
        response = self._request(
            "POST", f"/api/v1/wedrive/agent/sources/{source_id}/claim-scan", {"trigger": trigger},
            allow_statuses=(409,),
        )
        return response.status_code != 409

    def report_scan_failure(self, source_id: str, code: str) -> None:
        self._request("POST", f"/api/v1/wedrive/agent/sources/{source_id}/scan-failure", {"code": code})

    def begin_snapshot(self, source: dict, root_id: str, count: int) -> str:
        result = self._request("POST", "/api/v1/wedrive/agent/snapshots", {
            "source_id": source["id"], "sequence": int(time.time_ns() // 1_000_000),
            "root_external_id": root_id, "expected_item_count": count,
        }).json()
        return result["id"]

    def upload_items(self, snapshot_id: str, items: list[dict]) -> None:
        path = f"/api/v1/wedrive/agent/snapshots/{snapshot_id}/items"
        for offset in range(0, len(items), 250):
            self._request("POST", path, {"items": items[offset:offset + 250]})

    def commit(self, snapshot_id: str, count: int, stats: dict) -> None:
        path = f"/api/v1/wedrive/agent/snapshots/{snapshot_id}/commit"
        self._request("POST", path, {
            "item_count": count, "digest": "",
            "auto_share_existing": stats.get("existing", 0),
            "auto_share_created": stats.get("created", 0),
            "auto_share_failed": stats.get("failed", 0),
        })

    def websocket_headers(self, path: str) -> dict[str, str]:
        timestamp = str(int(time.time()))
        return {
            "X-WeDrive-Device-ID": self.identity.device_id,
            "X-WeDrive-Timestamp": timestamp,
            "X-WeDrive-Signature": self.identity.sign("GET", path, timestamp, b""),
            "X-WeDrive-Agent-Version": __version__,
        }

    def websocket_url(self, path: str) -> str:
        parsed = urlsplit(self.identity.server)
        scheme = "wss" if parsed.scheme == "https" else "ws"
        return f"{scheme}://{parsed.netloc}{path}"
