"""Run the real Python client against the signed Go HTTP acceptance server."""
import base64
import os
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from weknora_wedrive_agent import client as protocol
from weknora_wedrive_agent.client import APIClient, AgentUpgradeRequired, ScanAttemptConflict
from weknora_wedrive_agent.security import Identity


def expect_error(error, action):
    try:
        action()
    except error:
        return
    raise AssertionError(f"expected {error.__name__}")


def main():
    # Test-only ephemeral identity: exercise real signatures without writing
    # credentials or depending on the current user's DPAPI registration.
    identity = Identity(Path("unused-test-profile"))
    identity.server = sys.argv[1]
    identity.device_id = "device"
    identity.private_key = Ed25519PrivateKey.from_private_bytes(base64.b64decode(os.environ["WEDRIVE_TEST_KEY"]))
    api = APIClient(identity)
    try:
        assert len(api.list_sources()) == 1
        assert api.claim_scan("source", "scheduled") is None  # only manual
        first = api.claim_scan("source", "manual")
        assert first is not None
        renewed = api.renew_scan_lease("source", first.scan_attempt_id, 1)
        assert renewed.scan_attempt_id == first.scan_attempt_id
        items = [{"external_id": "root", "name": "root", "path": ".", "item_type": "folder"}]
        items += [{"external_id": f"file-{n}", "parent_external_id": "root", "name": f"file-{n}",
                   "path": f"file-{n}", "item_type": "file"} for n in range(251)]
        snapshot = api.begin_snapshot("source", first.scan_attempt_id, "root", len(items))
        assert api.begin_snapshot("source", first.scan_attempt_id, "root", len(items)) == snapshot
        api.upload_items(snapshot, items)
        api.commit(snapshot, len(items), {})
        source = api.list_sources()[0]
        assert source["last_snapshot_id"] == snapshot
        assert source["scan_state"] == "idle"

        second = api.claim_scan("source", "manual")
        assert second is not None and second.scan_attempt_id != first.scan_attempt_id
        interrupted = api.begin_snapshot("source", second.scan_attempt_id, "root", 1)
        api.report_scan_failure("source", second.scan_attempt_id, "tree_walk_failed")
        third = api.claim_scan("source", "manual")
        assert third is not None and third.scan_attempt_id != second.scan_attempt_id
        for action in [
            lambda: api.renew_scan_lease("source", second.scan_attempt_id, 2),
            lambda: api.report_scan_failure("source", second.scan_attempt_id, "late_failure"),
            lambda: api.begin_snapshot("source", second.scan_attempt_id, "root", 1),
            lambda: api.upload_items(interrupted, items[:1]),
            lambda: api.commit(interrupted, 1, {}),
        ]:
            expect_error(ScanAttemptConflict, action)
        api.commit(snapshot, len(items), {})  # completed retry is read-only

        protocol.__version__ = "0.3.1"  # actual old-version signed wire requests
        for action in [
            lambda: api.claim_scan("source", "manual"),
            lambda: api.renew_scan_lease("source", third.scan_attempt_id, 1),
            lambda: api.report_scan_failure("source", third.scan_attempt_id, "old_failure"),
            lambda: api.begin_snapshot("source", third.scan_attempt_id, "root", 1),
            lambda: api.upload_items(snapshot, items[:1]),
            lambda: api.commit(snapshot, len(items), {}),
        ]:
            expect_error(AgentUpgradeRequired, action)
        assert api.list_sources() == []
        protocol.__version__ = "0.4.0"
        current = api.list_sources()[0]
        assert current["scan_attempt_id"] == third.scan_attempt_id
        assert current["scan_state"] == "running"
        assert current["last_snapshot_id"] == snapshot
        api.report_scan_failure("source", third.scan_attempt_id, "acceptance_finished")
        print("Python/Go signed protocol acceptance passed: complete inventory, batches, stale writes, old tool gate")
    finally:
        api.http.close()


if __name__ == "__main__":
    main()
