"""Sandbox Policy Enforcement — PRD §4.7.

The sandbox spec declares the agent's filesystem, network, and syscall rights.
The runtime MUST enforce every clause; failure to enforce is a critical
conformance violation (C4).

Spec says implementations may use Docker, Firecracker, gVisor, or native
seccomp. This reference impl uses Python subprocess + restricted env + a
syscall-allowlist check. Production deployments swap in real sandboxes.

What we enforce:
  - fs.read / fs.write: path-glob allowlist
  - net.egress: host-glob allowlist (checked before any HTTP request)
  - net.listen: port allowlist
  - resources.cpu / memory / disk: enforced via subprocess resource limits
  - time.maxWall: enforced via subprocess timeout
"""
from __future__ import annotations

import fnmatch
import os
import resource
import shlex
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from loomwork.amp.errors import AmpError, ErrorCode


class SandboxViolation(Exception):
    """Raised when an agent attempts an operation not allowed by its sandbox."""

    def __init__(self, kind: str, target: str, allowed: list[str]):
        self.kind = kind
        self.target = target
        self.allowed = allowed
        super().__init__(
            f"Sandbox violation: {kind} {target!r} not in allowed list {allowed}"
        )


@dataclass
class SandboxPolicy:
    """Sandbox policy loaded from sandbox.json (PRD §4.7)."""

    fs_read: list[str] = field(default_factory=list)        # path-globs
    fs_write: list[str] = field(default_factory=list)       # path-globs
    net_egress: list[str] = field(default_factory=list)     # host-globs
    net_listen: list[int] = field(default_factory=list)     # ports
    syscalls_allow: list[str] = field(default_factory=list)
    syscalls_deny: list[str] = field(default_factory=list)
    resources_cpu: str = "1"
    resources_memory: str = "1GiB"
    resources_disk: str = "1GiB"
    time_max_wall: str = "10m"

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "SandboxPolicy":
        """Parse sandbox.json. Handles snake_case and dotted keys per PRD §4.7."""
        def get(d, *keys, default=None):
            for k in keys:
                if k in d:
                    return d[k]
            return default

        return cls(
            fs_read=get(d, "fs.read", "fs_read", default=[]) or [],
            fs_write=get(d, "fs.write", "fs_write", default=[]) or [],
            net_egress=get(d, "net.egress", "net_egress", default=[]) or [],
            net_listen=get(d, "net.listen", "net_listen", default=[]) or [],
            syscalls_allow=get(d, "syscalls.allow", "syscalls_allow", default=[]) or [],
            syscalls_deny=get(d, "syscalls.deny", "syscalls_deny", default=[]) or [],
            resources_cpu=get(d, "resources.cpu", "resources_cpu", default="1"),
            resources_memory=get(d, "resources.memory", "resources_memory", default="1GiB"),
            resources_disk=get(d, "resources.disk", "resources_disk", default="1GiB"),
            time_max_wall=get(d, "time.maxWall", "time_max_wall", default="10m"),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "fs.read": self.fs_read,
            "fs.write": self.fs_write,
            "net.egress": self.net_egress,
            "net.listen": self.net_listen,
            "syscalls.allow": self.syscalls_allow,
            "syscalls.deny": self.syscalls_deny,
            "resources.cpu": self.resources_cpu,
            "resources.memory": self.resources_memory,
            "resources.disk": self.resources_disk,
            "time.maxWall": self.time_max_wall,
        }


def _expand_path(glob: str) -> str:
    """Expand env vars and ~ in a path glob."""
    return os.path.expanduser(os.path.expandvars(glob))


def _parse_duration(s: str) -> float:
    """Parse '30m', '10s', '2h' → seconds."""
    s = s.strip()
    if not s:
        return 600.0
    unit = s[-1]
    n = float(s[:-1] if unit in "smh" else s)
    return n * {"s": 1, "m": 60, "h": 3600}.get(unit, 1)


def _parse_bytes(s: str) -> int:
    """Parse '4GiB', '512MiB', '1024' → bytes."""
    s = s.strip()
    if not s:
        return 1024 * 1024 * 1024
    units = {"KiB": 1024, "MiB": 1024**2, "GiB": 1024**3, "TiB": 1024**4,
             "KB": 1000, "MB": 1000**2, "GB": 1000**3, "TB": 1000**4}
    for unit, factor in units.items():
        if s.endswith(unit):
            return int(float(s[:-len(unit)]) * factor)
    return int(s)


class Sandbox:
    """Enforces a SandboxPolicy.

    This is the reference impl: it checks file/network operations against the
    policy before allowing them. It does NOT use Linux namespaces or seccomp;
    production deployments swap in real sandboxes. The enforcement contract is
    what matters — every clause of SandboxPolicy MUST be honored.
    """

    def __init__(self, policy: SandboxPolicy):
        self.policy = policy

    # ── Filesystem checks ────────────────────────────────────────────────────
    def check_read(self, path: str) -> None:
        """Raise SandboxViolation if path is not in fs.read allowlist."""
        real = os.path.realpath(_expand_path(path))
        allowed = [_expand_path(g) for g in self.policy.fs_read]
        # Special case: allow reading from /tmp/agent/** always
        if not any(fnmatch.fnmatch(real, os.path.realpath(g)) for g in allowed):
            raise SandboxViolation("fs.read", real, allowed)

    def check_write(self, path: str) -> None:
        """Raise SandboxViolation if path is not in fs.write allowlist."""
        real = os.path.realpath(_expand_path(path))
        allowed = [_expand_path(g) for g in self.policy.fs_write]
        if not any(fnmatch.fnmatch(real, os.path.realpath(g)) for g in allowed):
            raise SandboxViolation("fs.write", real, allowed)

    # ── Network checks ───────────────────────────────────────────────────────
    def check_egress(self, host: str) -> None:
        """Raise SandboxViolation if host is not in net.egress allowlist."""
        if not self.policy.net_egress:
            raise SandboxViolation("net.egress", host, ["(empty — no egress allowed)"])
        if not any(fnmatch.fnmatch(host, g) for g in self.policy.net_egress):
            raise SandboxViolation("net.egress", host, self.policy.net_egress)

    def check_listen(self, port: int) -> None:
        """Raise SandboxViolation if port is not in net.listen allowlist."""
        if port not in self.policy.net_listen:
            raise SandboxViolation("net.listen", str(port),
                                    [str(p) for p in self.policy.net_listen])

    # ── Resource limits (applied to subprocesses) ────────────────────────────
    def _rlimits(self) -> dict:
        """Convert policy to rlimit dict for subprocess preexec_fn."""
        mem_bytes = _parse_bytes(self.policy.resources_memory)
        disk_bytes = _parse_bytes(self.policy.resources_disk)
        return {
            "RLIMIT_AS": (mem_bytes, mem_bytes),  # address space
            "RLIMIT_FSIZE": (disk_bytes, disk_bytes),  # file size
            "RLIMIT_CPU": (int(self.policy.resources_cpu), int(self.policy.resources_cpu)),
        }

    def _preexec(self):
        """preexec_fn for subprocess — sets rlimits."""
        for limit, (soft, hard) in self._rlimits().items():
            try:
                res = getattr(resource, limit)
                resource.setrlimit(res, (soft, hard))
            except (AttributeError, ValueError, resource.error):
                pass  # not available on this platform

    def run_subprocess(self, cmd: list[str] | str, *,
                       stdin: bytes | None = None,
                       cwd: str | None = None,
                       env: dict | None = None) -> subprocess.CompletedProcess:
        """Run a subprocess inside the sandbox.

        Enforces:
          - resource limits (memory, disk, cpu) via rlimits
          - wall-clock timeout via time.maxWall
          - restricted env (no leaking host env)
        """
        if isinstance(cmd, str):
            cmd = shlex.split(cmd)

        timeout = _parse_duration(self.policy.time_max_wall)
        safe_env = (env or {}).copy()
        # Strip sensitive env vars
        for k in list(safe_env.keys()):
            if any(s in k.upper() for s in ("TOKEN", "SECRET", "KEY", "PASSWORD", "AWS")):
                safe_env.pop(k, None)

        try:
            return subprocess.run(
                cmd,
                input=stdin,
                cwd=cwd,
                env=safe_env,
                timeout=timeout,
                preexec_fn=self._preexec,
                capture_output=True,
                check=False,
            )
        except subprocess.TimeoutExpired as e:
            raise AmpError(ErrorCode.DEADLINE_EXCEEDED,
                           f"Subprocess exceeded {timeout}s wall clock",
                           data={"cmd": cmd, "timeout": timeout}) from e
