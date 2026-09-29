"""Tests for Personal Memory Layer."""
import pytest
from pathlib import Path

from loomwork.runtime.memory import MemoryLayer, MemoryEntry


def test_memory_write_and_get(tmp_path):
    mem = MemoryLayer(tmp_path / "mem.db", passphrase="test-pass")
    entry = MemoryEntry(
        agent_id="did:key:ed25519:abc",
        aci="loomwork.dev/test@v0.1",
        kind="episodic",
        content="Agent performed a search",
    )
    mem.write(entry)

    fetched = mem.get(entry.entry_id)
    assert fetched is not None
    assert fetched.content == "Agent performed a search"
    assert fetched.agent_id == "did:key:ed25519:abc"


def test_memory_encrypted_at_rest(tmp_path):
    """Content must NOT appear in plaintext in the DB file."""
    passphrase = "secret-pass"
    mem = MemoryLayer(tmp_path / "mem.db", passphrase=passphrase)
    secret_content = "THIS_IS_A_SECRET_STRING_THAT_SHOULD_NOT_APPEAR_IN_DB"
    mem.write(MemoryEntry(
        agent_id="agent_1", aci="test@v0.1", kind="episodic",
        content=secret_content,
    ))
    mem.close()

    raw = (tmp_path / "mem.db").read_bytes()
    assert secret_content.encode() not in raw, "Content appears in plaintext!"


def test_memory_unencrypted_when_no_passphrase(tmp_path):
    """Without a passphrase, content is stored plaintext (dev mode)."""
    mem = MemoryLayer(tmp_path / "mem.db", passphrase=None)
    mem.write(MemoryEntry(
        agent_id="agent_1", aci="test@v0.1", kind="episodic",
        content="PLAINTEXT_CONTENT",
    ))
    mem.close()
    raw = (tmp_path / "mem.db").read_bytes()
    assert b"PLAINTEXT_CONTENT" in raw


def test_memory_query_by_agent(tmp_path):
    mem = MemoryLayer(tmp_path / "mem.db", passphrase="x")
    mem.write(MemoryEntry(agent_id="agent_a", aci="x", kind="episodic", content="a1"))
    mem.write(MemoryEntry(agent_id="agent_a", aci="x", kind="episodic", content="a2"))
    mem.write(MemoryEntry(agent_id="agent_b", aci="x", kind="episodic", content="b1"))

    a_entries = mem.query_by_agent("agent_a")
    assert len(a_entries) == 2
    b_entries = mem.query_by_agent("agent_b")
    assert len(b_entries) == 1


def test_memory_graph_triples(tmp_path):
    mem = MemoryLayer(tmp_path / "mem.db", passphrase="x")
    mem.add_graph_triple("loomwork", "depends_on", "MCP")
    mem.add_graph_triple("loomwork", "depends_on", "OCI")
    mem.add_graph_triple("loomwork", "spec_version", "v0.1")

    deps = mem.query_graph("loomwork", "depends_on")
    assert len(deps) == 2
    assert ("loomwork", "depends_on", "MCP") in deps
    assert ("loomwork", "depends_on", "OCI") in deps


def test_memory_episodic_timeline(tmp_path):
    mem = MemoryLayer(tmp_path / "mem.db", passphrase="x")
    for i in range(5):
        mem.write(MemoryEntry(agent_id="a", aci="x", kind="episodic",
                              content=f"event-{i}"))
    timeline = mem.episodic_timeline(limit=3)
    assert len(timeline) == 3
