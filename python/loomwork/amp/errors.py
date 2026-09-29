"""AMP Error Code Registry — PRD §5.9.

The error code space is partitioned:
  1xx — transport
  2xx — capability
  3xx — task
  4xx — credit
  5xx — protocol
"""
from __future__ import annotations

from dataclasses import dataclass
from enum import IntEnum
from typing import Optional


class ErrorCode(IntEnum):
    """All AMP error codes from PRD §5.9."""

    # 1xx — transport
    TRANSPORT_ERROR = 100
    TTL_EXPIRED = 101

    # 2xx — capability
    CAPABILITY_MISSING = 200
    CAPABILITY_EXPIRED = 201
    CAPABILITY_REVOKED = 202

    # 3xx — task
    TASK_NOT_FOUND = 300
    TASK_ALREADY_COMPLETE = 301
    BUDGET_EXCEEDED = 302
    DEADLINE_EXCEEDED = 303

    # 4xx — credit
    CURRENCY_MISMATCH = 400
    INVALID_RECEIPT = 401
    INSUFFICIENT_BALANCE = 402

    # 5xx — protocol
    PROTOCOL_VIOLATION = 500
    VERSION_MISMATCH = 501
    CYCLE_DETECTED = 502


@dataclass(frozen=True)
class ErrorSpec:
    code: ErrorCode
    name: str
    description: str
    retry: bool
    retry_strategy: str = ""


ERROR_REGISTRY: dict[ErrorCode, ErrorSpec] = {
    ErrorCode.TRANSPORT_ERROR: ErrorSpec(
        ErrorCode.TRANSPORT_ERROR, "TRANSPORT_ERROR",
        "Underlying transport failure (WS close, libp2p reset).",
        retry=True, retry_strategy="Yes, backoff"),
    ErrorCode.TTL_EXPIRED: ErrorSpec(
        ErrorCode.TTL_EXPIRED, "TTL_EXPIRED",
        "Message arrived after amp.ttl window.",
        retry=False),
    ErrorCode.CAPABILITY_MISSING: ErrorSpec(
        ErrorCode.CAPABILITY_MISSING, "CAPABILITY_MISSING",
        "Sender did not present a required capability token.",
        retry=False),
    ErrorCode.CAPABILITY_EXPIRED: ErrorSpec(
        ErrorCode.CAPABILITY_EXPIRED, "CAPABILITY_EXPIRED",
        "Capability token's expiresAt is in the past.",
        retry=True, retry_strategy="Yes, after refresh"),
    ErrorCode.CAPABILITY_REVOKED: ErrorSpec(
        ErrorCode.CAPABILITY_REVOKED, "CAPABILITY_REVOKED",
        "Capability was revoked via amp/revoke.",
        retry=False),
    ErrorCode.TASK_NOT_FOUND: ErrorSpec(
        ErrorCode.TASK_NOT_FOUND, "TASK_NOT_FOUND",
        "Referenced taskId does not exist in the callee's graph.",
        retry=False),
    ErrorCode.TASK_ALREADY_COMPLETE: ErrorSpec(
        ErrorCode.TASK_ALREADY_COMPLETE, "TASK_ALREADY_COMPLETE",
        "Callee received a delegate for an already-complete task.",
        retry=False),
    ErrorCode.BUDGET_EXCEEDED: ErrorSpec(
        ErrorCode.BUDGET_EXCEEDED, "BUDGET_EXCEEDED",
        "Task budget has been consumed.",
        retry=True, retry_strategy="Yes, after negotiate"),
    ErrorCode.DEADLINE_EXCEEDED: ErrorSpec(
        ErrorCode.DEADLINE_EXCEEDED, "DEADLINE_EXCEEDED",
        "Task deadline passed before completion.",
        retry=False),
    ErrorCode.CURRENCY_MISMATCH: ErrorSpec(
        ErrorCode.CURRENCY_MISMATCH, "CURRENCY_MISMATCH",
        "Payer and payee use different credit currencies.",
        retry=False),
    ErrorCode.INVALID_RECEIPT: ErrorSpec(
        ErrorCode.INVALID_RECEIPT, "INVALID_RECEIPT",
        "Credit receipt signature failed verification.",
        retry=False),
    ErrorCode.INSUFFICIENT_BALANCE: ErrorSpec(
        ErrorCode.INSUFFICIENT_BALANCE, "INSUFFICIENT_BALANCE",
        "Payer's declared balance is below receipt amount.",
        retry=False),
    ErrorCode.PROTOCOL_VIOLATION: ErrorSpec(
        ErrorCode.PROTOCOL_VIOLATION, "PROTOCOL_VIOLATION",
        "Message does not conform to AMP envelope schema.",
        retry=False),
    ErrorCode.VERSION_MISMATCH: ErrorSpec(
        ErrorCode.VERSION_MISMATCH, "VERSION_MISMATCH",
        "amp.version not supported by peer.",
        retry=False),
    ErrorCode.CYCLE_DETECTED: ErrorSpec(
        ErrorCode.CYCLE_DETECTED, "CYCLE_DETECTED",
        "Delegation would create a cycle in task graph.",
        retry=False),
}


class AmpError(Exception):
    """Exception carrying an AMP error code."""

    def __init__(self, code: ErrorCode, message: str, *, data: dict | None = None):
        self.code = code
        self.data = data or {}
        super().__init__(message)

    def to_dict(self) -> dict:
        return {
            "code": self.code.value,
            "message": str(self),
            "data": self.data,
        }

    @property
    def spec(self) -> ErrorSpec:
        return ERROR_REGISTRY[self.code]
