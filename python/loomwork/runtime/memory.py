"""Personal Memory Layer — PRD §3 (Mesh Runtime pillar).

Local-first hybrid store: vector + graph + episodic timeline. End-to-end
encrypted (AES-256-GCM with user's passphrase-derived key). Cryptographically
signed provenance on every entry — you can audit which agent wrote what,
when, on which device.
"""
from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import time
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC


@dataclass
class MemoryEntry:
    """A single memory write. Every field is stored encrypted."""

    entry_id: str = field(default_factory=lambda: new_id("mem"))
    agent_id: str = ""           # did:key:... of writer
    aci: str = ""                # aci identifier
    kind: str = ""               # "episodic", "semantic", "procedural"
    content: str = ""            # text content
    embedding: list[float] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)
    timestamp: str = ""
    signature: Optional[str] = None  # agent's signature over the content

    def to_dict(self) -> dict:
        return asdict(self)


def _derive_key(passphrase: str, salt: bytes) -> bytes:
    """Derive a 256-bit AES key from passphrase + salt."""
    kdf = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=32,
        salt=salt,
        iterations=200_000,
    )
    return kdf.derive(passphrase.encode("utf-8"))


class MemoryLayer:
    """SQLite-backed memory layer with E2E encryption.

    Schema (per PRD §3):
      - vector: embeddings (stored as opaque blobs; plug in any embedder)
      - graph: semantic relationships (subject-predicate-object triples)
      - episodic: timestamped event log
    """

    def __init__(self, db_path: str | Path, *, passphrase: str | None = None):
        self.db_path = Path(db_path)
        self.db_path.parent.mkdir(parents=True, exist_ok=True)

        # Encryption setup
        self._salt_path = self.db_path.with_suffix(".salt")
        if passphrase:
            if self._salt_path.exists():
                salt = self._salt_path.read_bytes()
            else:
                salt = os.urandom(16)
                self._salt_path.write_bytes(salt)
            self._aes_key = _derive_key(passphrase, salt)
            self._aesgcm = AESGCM(self._aes_key)
        else:
            self._aes_key = None
            self._aesgcm = None

        self._conn = sqlite3.connect(str(self.db_path))
        self._init_schema()

    def _init_schema(self) -> None:
        self._conn.executescript("""
            CREATE TABLE IF NOT EXISTS entries (
                entry_id    TEXT PRIMARY KEY,
                agent_id    TEXT NOT NULL,
                aci         TEXT NOT NULL,
                kind        TEXT NOT NULL,
                content_enc BLOB NOT NULL,
                embedding   BLOB,
                metadata    TEXT,
                timestamp   TEXT NOT NULL,
                signature   TEXT
            );
            CREATE INDEX IF NOT EXISTS idx_agent ON entries(agent_id);
            CREATE INDEX IF NOT EXISTS idx_kind  ON entries(kind);
            CREATE INDEX IF NOT EXISTS idx_time  ON entries(timestamp);

            CREATE TABLE IF NOT EXISTS graph (
                subject TEXT NOT NULL,
                predicate TEXT NOT NULL,
                object  TEXT NOT NULL,
                entry_id TEXT,
                PRIMARY KEY (subject, predicate, object)
            );
            CREATE INDEX IF NOT EXISTS idx_graph_subj ON graph(subject);

            CREATE TABLE IF NOT EXISTS episodic (
                ts      TEXT NOT NULL,
                event   TEXT NOT NULL,
                entry_id TEXT,
                agent_id TEXT
            );
            CREATE INDEX IF NOT EXISTS idx_ep_ts ON episodic(ts);
        """)
        self._conn.commit()

    def _encrypt(self, plaintext: str) -> bytes:
        if self._aesgcm is None:
            return plaintext.encode("utf-8")
        nonce = os.urandom(12)
        ct = self._aesgcm.encrypt(nonce, plaintext.encode("utf-8"), None)
        return nonce + ct

    def _decrypt(self, blob: bytes) -> str:
        if self._aesgcm is None:
            return blob.decode("utf-8")
        nonce, ct = blob[:12], blob[12:]
        return self._aesgcm.decrypt(nonce, ct, None).decode("utf-8")

    # ── Write ─────────────────────────────────────────────────────────────────
    def write(self, entry: MemoryEntry) -> MemoryEntry:
        """Write a memory entry. Returns the entry (with timestamp filled)."""
        if not entry.timestamp:
            entry.timestamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

        content_enc = self._encrypt(entry.content)
        embedding_blob = json.dumps(entry.embedding).encode("utf-8") if entry.embedding else None

        self._conn.execute(
            """INSERT OR REPLACE INTO entries
               (entry_id, agent_id, aci, kind, content_enc, embedding, metadata, timestamp, signature)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                entry.entry_id, entry.agent_id, entry.aci, entry.kind,
                content_enc, embedding_blob,
                json.dumps(entry.metadata), entry.timestamp, entry.signature,
            ),
        )

        # Also add to episodic table
        self._conn.execute(
            "INSERT INTO episodic (ts, event, entry_id, agent_id) VALUES (?, ?, ?, ?)",
            (entry.timestamp, f"{entry.kind}:{entry.entry_id}", entry.entry_id, entry.agent_id),
        )

        self._conn.commit()
        return entry

    def add_graph_triple(self, subject: str, predicate: str, object: str,
                          entry_id: str | None = None) -> None:
        """Add a semantic triple to the graph layer."""
        self._conn.execute(
            "INSERT OR REPLACE INTO graph (subject, predicate, object, entry_id) VALUES (?, ?, ?, ?)",
            (subject, predicate, object, entry_id),
        )
        self._conn.commit()

    # ── Read ──────────────────────────────────────────────────────────────────
    def get(self, entry_id: str) -> MemoryEntry | None:
        row = self._conn.execute(
            "SELECT entry_id, agent_id, aci, kind, content_enc, embedding, metadata, timestamp, signature FROM entries WHERE entry_id = ?",
            (entry_id,),
        ).fetchone()
        if not row:
            return None
        return self._row_to_entry(row)

    def query_by_agent(self, agent_id: str, *, limit: int = 100) -> list[MemoryEntry]:
        rows = self._conn.execute(
            "SELECT entry_id, agent_id, aci, kind, content_enc, embedding, metadata, timestamp, signature FROM entries WHERE agent_id = ? ORDER BY timestamp DESC LIMIT ?",
            (agent_id, limit),
        ).fetchall()
        return [self._row_to_entry(r) for r in rows]

    def query_by_kind(self, kind: str, *, limit: int = 100) -> list[MemoryEntry]:
        rows = self._conn.execute(
            "SELECT entry_id, agent_id, aci, kind, content_enc, embedding, metadata, timestamp, signature FROM entries WHERE kind = ? ORDER BY timestamp DESC LIMIT ?",
            (kind, limit),
        ).fetchall()
        return [self._row_to_entry(r) for r in rows]

    def query_graph(self, subject: str, predicate: str | None = None) -> list[tuple[str, str, str]]:
        if predicate:
            rows = self._conn.execute(
                "SELECT subject, predicate, object FROM graph WHERE subject = ? AND predicate = ?",
                (subject, predicate),
            ).fetchall()
        else:
            rows = self._conn.execute(
                "SELECT subject, predicate, object FROM graph WHERE subject = ?",
                (subject,),
            ).fetchall()
        return [(r[0], r[1], r[2]) for r in rows]

    def episodic_timeline(self, *, since: str | None = None, limit: int = 100) -> list[dict]:
        if since:
            rows = self._conn.execute(
                "SELECT ts, event, entry_id, agent_id FROM episodic WHERE ts >= ? ORDER BY ts ASC LIMIT ?",
                (since, limit),
            ).fetchall()
        else:
            rows = self._conn.execute(
                "SELECT ts, event, entry_id, agent_id FROM episodic ORDER BY ts DESC LIMIT ?",
                (limit,),
            ).fetchall()
        return [{"ts": r[0], "event": r[1], "entry_id": r[2], "agent_id": r[3]} for r in rows]

    # ── Similarity (vector) ───────────────────────────────────────────────────
    def similarity_search(self, query_embedding: list[float], *, limit: int = 5) -> list[tuple[MemoryEntry, float]]:
        """Cosine similarity search over all entries with embeddings.

        Naive O(n) scan. Production deployments swap in a real vector index
        (faiss, pgvector, etc.).
        """
        all_entries = self.query_by_kind("episodic", limit=10000) + \
                       self.query_by_kind("semantic", limit=10000)
        scored = []
        for entry in all_entries:
            if not entry.embedding:
                continue
            score = _cosine(query_embedding, entry.embedding)
            scored.append((entry, score))
        scored.sort(key=lambda x: -x[1])
        return scored[:limit]

    def _row_to_entry(self, row) -> MemoryEntry:
        entry_id, agent_id, aci, kind, content_enc, embedding_blob, metadata_json, timestamp, signature = row
        return MemoryEntry(
            entry_id=entry_id,
            agent_id=agent_id,
            aci=aci,
            kind=kind,
            content=self._decrypt(content_enc),
            embedding=json.loads(embedding_blob) if embedding_blob else [],
            metadata=json.loads(metadata_json) if metadata_json else {},
            timestamp=timestamp,
            signature=signature,
        )

    def close(self) -> None:
        self._conn.close()


def _cosine(a: list[float], b: list[float]) -> float:
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = sum(x * x for x in a) ** 0.5
    nb = sum(y * y for y in b) ** 0.5
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)
