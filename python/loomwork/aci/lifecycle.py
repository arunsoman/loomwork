"""ACI Lifecycle State Machine.

Spec: §4.9 of Loomwork PRD v0.1.

States: cold → warm → live → suspended → terminated

Transitions:
    load + verify sig   cold    -> warm
    first invocation    warm    -> live
    idle timeout / OOM  live    -> suspended
    on_resume           suspended -> warm
    shutdown / revoke   ANY     -> terminated
    on_terminate        (terminal; no transitions out)

Every transition emits a signed LifecycleEvent that subscribers can react to.
"""
from __future__ import annotations

import time
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Optional

from loomwork._ids import new_ulid, new_id


class LifecycleState(str, Enum):
    """The five lifecycle states per PRD §4.9."""

    COLD = "cold"            # signed package, not yet loaded
    WARM = "warm"            # manifest cached, sandbox prepared
    LIVE = "live"            # processing tasks, memory online
    SUSPENDED = "suspended"  # state snapshotted, RAM released
    TERMINATED = "terminated"  # sandbox torn down, logs sealed


# Allowed transitions
ALLOWED_TRANSITIONS: dict[LifecycleState, set[LifecycleState]] = {
    LifecycleState.COLD: {LifecycleState.WARM, LifecycleState.TERMINATED},
    LifecycleState.WARM: {LifecycleState.LIVE, LifecycleState.SUSPENDED, LifecycleState.TERMINATED},
    LifecycleState.LIVE: {LifecycleState.SUSPENDED, LifecycleState.TERMINATED},
    LifecycleState.SUSPENDED: {LifecycleState.WARM, LifecycleState.TERMINATED},
    LifecycleState.TERMINATED: set(),  # terminal
}


class LifecycleTransitionError(Exception):
    """Raised when an illegal state transition is attempted."""


@dataclass
class LifecycleEvent:
    """A single lifecycle transition event.

    Per PRD §4.9: 'Every transition emits a signed event that subscribers
    (the mesh runtime, the UI, telemetry) can react to.'
    """

    event_id: str
    agent_id: str
    from_state: LifecycleState
    to_state: LifecycleState
    trigger: str  # e.g. "on_load", "on_activate", "on_suspend", "on_resume", "on_terminate"
    timestamp: str
    metadata: dict = field(default_factory=dict)
    signature: Optional[str] = None  # populated when signed

    def to_dict(self) -> dict:
        return {
            "event_id": self.event_id,
            "agent_id": self.agent_id,
            "from_state": self.from_state.value,
            "to_state": self.to_state.value,
            "trigger": self.trigger,
            "timestamp": self.timestamp,
            "metadata": self.metadata,
            "signature": self.signature,
        }


# Trigger names (PRD §4.9 + figure 2)
TRIGGER_LOAD = "on_load"
TRIGGER_ACTIVATE = "on_activate"
TRIGGER_SUSPEND = "on_suspend"
TRIGGER_RESUME = "on_resume"
TRIGGER_TERMINATE = "on_terminate"


class LifecycleMachine:
    """State machine for a single ACI instance.

    Thread-unsafe; the orchestrator is responsible for serializing access.
    """

    def __init__(self, agent_id: str):
        self.agent_id = agent_id
        self._state = LifecycleState.COLD
        self._events: list[LifecycleEvent] = []
        self._subscribers: list[Callable[[LifecycleEvent], None]] = []

    @property
    def state(self) -> LifecycleState:
        return self._state

    @property
    def events(self) -> list[LifecycleEvent]:
        return list(self._events)

    def subscribe(self, callback: Callable[[LifecycleEvent], None]) -> None:
        """Register a callback for lifecycle events."""
        self._subscribers.append(callback)

    def transition(self, to: LifecycleState, trigger: str, metadata: dict | None = None) -> LifecycleEvent:
        """Attempt a state transition. Raises if not allowed."""
        if to not in ALLOWED_TRANSITIONS.get(self._state, set()):
            raise LifecycleTransitionError(
                f"Cannot transition from {self._state.value!r} to {to.value!r}"
            )

        event = LifecycleEvent(
            event_id=str(new_ulid()),
            agent_id=self.agent_id,
            from_state=self._state,
            to_state=to,
            trigger=trigger,
            timestamp=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            metadata=metadata or {},
        )

        self._state = to
        self._events.append(event)
        self._notify(event)
        return event

    # ── Convenience methods ──────────────────────────────────────────────────
    def load(self) -> LifecycleEvent:
        return self.transition(LifecycleState.WARM, TRIGGER_LOAD)

    def activate(self) -> LifecycleEvent:
        return self.transition(LifecycleState.LIVE, TRIGGER_ACTIVATE)

    def suspend(self, reason: str = "idle_timeout") -> LifecycleEvent:
        return self.transition(LifecycleState.SUSPENDED, TRIGGER_SUSPEND, {"reason": reason})

    def resume(self) -> LifecycleEvent:
        return self.transition(LifecycleState.WARM, TRIGGER_RESUME)

    def terminate(self, reason: str = "shutdown") -> LifecycleEvent:
        return self.transition(LifecycleState.TERMINATED, TRIGGER_TERMINATE, {"reason": reason})

    def _notify(self, event: LifecycleEvent) -> None:
        for cb in self._subscribers:
            try:
                cb(event)
            except Exception:
                pass  # subscribers must not crash the state machine
