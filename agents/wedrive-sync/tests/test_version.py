import subprocess
import sys


def test_version_exits_without_registration_or_browser():
    result = subprocess.run(
        [sys.executable, "-m", "weknora_wedrive_agent.main", "--version"],
        capture_output=True, text=True, timeout=10,
    )
    assert result.returncode == 0, result.stderr
    assert result.stdout.strip() == "WeKnora WeDrive Sync Tool 0.4.2"
