import httpx
import json
from datetime import datetime, timezone
import pytest

from weknora_wedrive_agent import __version__
from weknora_wedrive_agent.client import APIClient, AgentUpgradeRequired, ScanAttemptConflict


class FakeIdentity:
    device_id = "device-1"
    server = "http://agent.test"

    @staticmethod
    def sign(method: str, path: str, timestamp: str, body: bytes) -> str:
        return "signature"


def test_claim_scan_treats_server_conflict_as_not_claimed() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.headers["X-WeDrive-Agent-Version"] == __version__
        assert request.url.path.endswith("/sources/source-1/claim-scan")
        return httpx.Response(409, json={"error": "wedrive scan is not due"})

    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(handler))
    try:
        assert client.claim_scan("source-1", "scheduled") is None
    finally:
        client.http.close()


@pytest.mark.parametrize("response", [
    {}, {"scan_attempt_id": ""},
    {"scan_attempt_id": "attempt", "scan_lease_expires_at": "2026-09-26T10:30:00"},
])
def test_claim_without_identity_and_timezone_cannot_start_scan(response: dict) -> None:
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(lambda request: httpx.Response(200, json=response)))
    try:
        with pytest.raises(ValueError):
            client.claim_scan("source-1", "manual")
    finally:
        client.http.close()


def test_claim_identity_is_returned_and_carried_to_failure_report() -> None:
    requests = []
    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if request.url.path.endswith("claim-scan"):
            return httpx.Response(200, json={"scan_attempt_id": "attempt-1", "scan_lease_expires_at": "2026-09-26T10:30:00Z"})
        assert json.loads(request.content) == {"code": "listing_timeout", "scan_attempt_id": "attempt-1"}
        return httpx.Response(200, json={})
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(handler))
    try:
        claim = client.claim_scan("source-1", "manual")
        assert claim is not None
        assert claim.scan_attempt_id == "attempt-1"
        assert claim.lease_expires_at == datetime(2026, 9, 26, 10, 30, tzinfo=timezone.utc)
        client.report_scan_failure("source-1", claim.scan_attempt_id, "listing_timeout")
        assert len(requests) == 2
    finally:
        client.http.close()


def test_upgrade_rejection_is_distinct_from_an_unavailable_scan() -> None:
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(lambda request: httpx.Response(426, json={"code": "agent_upgrade_required", "minimum_agent_version": "0.4.0"})))
    try:
        with pytest.raises(AgentUpgradeRequired):
            client.claim_scan("source-1", "manual")
    finally:
        client.http.close()


def test_begin_snapshot_carries_the_claimed_attempt_identity() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        payload = json.loads(request.content)
        assert request.url.path == "/api/v1/wedrive/agent/snapshots"
        assert payload["source_id"] == "source-1"
        assert payload["scan_attempt_id"] == "attempt-1"
        assert payload["root_external_id"] == "root"
        assert payload["expected_item_count"] == 1
        return httpx.Response(201, json={"id": "snapshot-1"})
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(handler))
    try:
        assert client.begin_snapshot("source-1", "attempt-1", "root", 1) == "snapshot-1"
    finally:
        client.http.close()


def test_upload_stops_after_losing_the_scan_attempt() -> None:
    requests = []
    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(409, json={"code": "scan_attempt_conflict"})
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(handler))
    try:
        with pytest.raises(ScanAttemptConflict):
            client.upload_items("snapshot-1", [{"external_id": str(i)} for i in range(501)])
        assert len(requests) == 1
    finally:
        client.http.close()
