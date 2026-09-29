"""ID generation helper.

Wraps ulid-py to work around the API quirk where ulid.ULID() (the class
constructor) expects a buffer argument, while ulid.new() (the factory)
returns a fresh ULID. All Loomwork code should use `new_id(prefix=...)`
instead of touching the ulid library directly.
"""
from __future__ import annotations

import ulid as _ulid


def new_ulid() -> str:
    """Return a new ULID as a 26-char base32 string."""
    return str(_ulid.new())


def new_id(prefix: str) -> str:
    """Return a prefixed ID like 'task_01H8XJZK...'."""
    return f"{prefix}_{new_ulid()}"
