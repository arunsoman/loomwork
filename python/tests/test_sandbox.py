"""Tests for sandbox policy enforcement (PRD §4.7, conformance C4)."""
import os
import pytest
from pathlib import Path

from loomwork.runtime.sandbox import Sandbox, SandboxPolicy, SandboxViolation
from loomwork.amp.errors import AmpError, ErrorCode


def test_sandbox_allows_read_in_allowlist(tmp_path):
    f = tmp_path / "allowed.txt"
    f.write_text("hello")
    policy = SandboxPolicy(fs_read=[str(tmp_path / "**")])
    s = Sandbox(policy)
    s.check_read(str(f))  # should not raise


def test_sandbox_blocks_read_outside_allowlist(tmp_path):
    f = tmp_path / "secret.txt"
    f.write_text("shh")
    policy = SandboxPolicy(fs_read=[])  # empty allowlist
    s = Sandbox(policy)
    with pytest.raises(SandboxViolation):
        s.check_read(str(f))


def test_sandbox_allows_write_in_allowlist(tmp_path):
    f = tmp_path / "out.txt"
    policy = SandboxPolicy(fs_write=[str(tmp_path / "**")])
    s = Sandbox(policy)
    s.check_write(str(f))


def test_sandbox_blocks_write_outside_allowlist(tmp_path):
    f = tmp_path / "out.txt"
    policy = SandboxPolicy(fs_write=[])
    s = Sandbox(policy)
    with pytest.raises(SandboxViolation):
        s.check_write(str(f))


def test_sandbox_allows_egress_in_allowlist():
    policy = SandboxPolicy(net_egress=["*.wikipedia.org", "api.github.com"])
    s = Sandbox(policy)
    s.check_egress("en.wikipedia.org")
    s.check_egress("api.github.com")


def test_sandbox_blocks_egress_outside_allowlist():
    policy = SandboxPolicy(net_egress=["*.wikipedia.org"])
    s = Sandbox(policy)
    with pytest.raises(SandboxViolation):
        s.check_egress("evil.com")


def test_sandbox_empty_egress_blocks_all():
    policy = SandboxPolicy(net_egress=[])
    s = Sandbox(policy)
    with pytest.raises(SandboxViolation):
        s.check_egress("anything.com")


def test_sandbox_listen_port():
    policy = SandboxPolicy(net_listen=[8080, 9090])
    s = Sandbox(policy)
    s.check_listen(8080)
    with pytest.raises(SandboxViolation):
        s.check_listen(3000)


def test_sandbox_from_dict_dotted_keys():
    """PRD §4.7 uses dotted keys: fs.read, net.egress, etc."""
    policy = SandboxPolicy.from_dict({
        "fs.read": ["/tmp/**"],
        "fs.write": ["/tmp/out/**"],
        "net.egress": ["*.wikipedia.org"],
        "net.listen": [8080],
        "resources.cpu": "2",
        "resources.memory": "4GiB",
        "time.maxWall": "30m",
    })
    assert policy.fs_read == ["/tmp/**"]
    assert policy.net_egress == ["*.wikipedia.org"]
    assert policy.resources_cpu == "2"
    assert policy.time_max_wall == "30m"


def test_sandbox_run_subprocess_timeout():
    """Subprocess that exceeds time.maxWall raises DEADLINE_EXCEEDED."""
    policy = SandboxPolicy.from_dict({"time.maxWall": "1s"})
    s = Sandbox(policy)
    with pytest.raises(AmpError) as exc:
        s.run_subprocess(["sleep", "5"])
    assert exc.value.code == ErrorCode.DEADLINE_EXCEEDED
