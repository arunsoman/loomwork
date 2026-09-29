"""AMP Task Graph — PRD §5.6.

Every delegated task becomes a node in a shared task graph. The graph is a
DAG: tasks may spawn subtasks, but cycles are forbidden and the runtime
MUST detect and reject them.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Optional
from loomwork._ids import new_ulid, new_id

from loomwork.amp.errors import AmpError, ErrorCode


class TaskStatus(str, Enum):
    PENDING = "pending"
    RUNNING = "running"
    NEGOTIATING = "negotiating"
    COMPLETE = "complete"
    FAILED = "failed"
    CANCELLED = "cancelled"


@dataclass
class TaskNode:
    """A single node in the task graph."""

    task_id: str = field(default_factory=lambda: new_id("task"))
    parent_id: Optional[str] = None
    intent: str = ""  # e.g. "research", "code", "deploy"
    inputs: dict = field(default_factory=dict)
    expected_outputs: list[str] = field(default_factory=list)
    deadline: Optional[str] = None
    budget: dict = field(default_factory=dict)
    status: TaskStatus = TaskStatus.PENDING
    budget_consumed: dict = field(default_factory=dict)
    artifacts: list[dict] = field(default_factory=list)
    delegatee: Optional[str] = None  # did:key:...
    error: Optional[str] = None

    def to_dict(self) -> dict:
        return {
            "task_id": self.task_id,
            "parent_id": self.parent_id,
            "intent": self.intent,
            "inputs": self.inputs,
            "expected_outputs": self.expected_outputs,
            "deadline": self.deadline,
            "budget": self.budget,
            "status": self.status.value,
            "budget_consumed": self.budget_consumed,
            "artifacts": self.artifacts,
            "delegatee": self.delegatee,
            "error": self.error,
        }


class TaskGraph:
    """DAG of delegated tasks. Cycle detection on insert."""

    def __init__(self) -> None:
        self._nodes: dict[str, TaskNode] = {}

    def add_task(self, task: TaskNode) -> TaskNode:
        """Add a task. Raises CYCLE_DETECTED if it would create a cycle."""
        if task.parent_id and task.parent_id not in self._nodes:
            raise AmpError(
                ErrorCode.TASK_NOT_FOUND,
                f"Parent task {task.parent_id!r} not in graph",
            )
        # Cycle check: walk up parent chain, ensure we don't loop back to ourselves
        if task.parent_id:
            visited = {task.task_id}
            current = task.parent_id
            while current is not None:
                if current in visited:
                    raise AmpError(
                        ErrorCode.CYCLE_DETECTED,
                        f"Adding task {task.task_id!r} would create a cycle",
                    )
                visited.add(current)
                parent = self._nodes.get(current)
                current = parent.parent_id if parent else None
        self._nodes[task.task_id] = task
        return task

    def get(self, task_id: str) -> TaskNode:
        if task_id not in self._nodes:
            raise AmpError(ErrorCode.TASK_NOT_FOUND, f"Task {task_id!r} not in graph")
        return self._nodes[task_id]

    def update_status(self, task_id: str, status: TaskStatus) -> TaskNode:
        task = self.get(task_id)
        # Cannot mutate a terminal task
        if task.status in (TaskStatus.COMPLETE, TaskStatus.FAILED, TaskStatus.CANCELLED):
            raise AmpError(
                ErrorCode.TASK_ALREADY_COMPLETE,
                f"Task {task_id!r} is already in terminal state {task.status.value!r}",
            )
        task.status = status
        return task

    def add_artifact(self, task_id: str, artifact: dict) -> None:
        task = self.get(task_id)
        task.artifacts.append(artifact)

    def consume_budget(self, task_id: str, tokens: int = 0, tool_calls: int = 0,
                       wall_seconds: int = 0) -> dict:
        """Accumulate budget consumption. Returns total consumed so far."""
        task = self.get(task_id)
        task.budget_consumed["tokens"] = task.budget_consumed.get("tokens", 0) + tokens
        task.budget_consumed["toolCalls"] = task.budget_consumed.get("toolCalls", 0) + tool_calls
        task.budget_consumed["wallSeconds"] = task.budget_consumed.get("wallSeconds", 0) + wall_seconds

        # Check budget
        max_tokens = task.budget.get("maxTokens")
        if max_tokens and task.budget_consumed["tokens"] > max_tokens:
            raise AmpError(ErrorCode.BUDGET_EXCEEDED,
                           f"Token budget {max_tokens} exceeded",
                           data={"consumed": task.budget_consumed})

        return dict(task.budget_consumed)

    def all_tasks(self) -> list[TaskNode]:
        return list(self._nodes.values())

    def children(self, task_id: str) -> list[TaskNode]:
        """Return all direct children of a task."""
        return [t for t in self._nodes.values() if t.parent_id == task_id]
