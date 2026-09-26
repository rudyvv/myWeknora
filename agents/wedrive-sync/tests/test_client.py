import httpx

from weknora_wedrive_agent import __version__
from weknora_wedrive_agent.client import APIClient


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
        assert client.claim_scan("source-1", "scheduled") is False
    finally:
        client.http.close()


def test_claim_scan_returns_true_when_server_grants_lease() -> None:
    client = APIClient(FakeIdentity())  # type: ignore[arg-type]
    client.http = httpx.Client(base_url=FakeIdentity.server, transport=httpx.MockTransport(lambda request: httpx.Response(200, json={})))
    try:
        assert client.claim_scan("source-1", "manual") is True
    finally:
        client.http.close()
