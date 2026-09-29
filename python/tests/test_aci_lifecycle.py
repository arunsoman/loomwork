"""Tests for ACI lifecycle state machine."""
import pytest

from loomwork.aci.lifecycle import (
    LifecycleMachine, LifecycleState, LifecycleTransitionError,
)


def test_initial_state_is_cold():
    m = LifecycleMachine("agent_1")
    assert m.state == LifecycleState.COLD


def test_happy_path():
    m = LifecycleMachine("agent_1")
    m.load()        # cold → warm
    assert m.state == LifecycleState.WARM
    m.activate()    # warm → live
    assert m.state == LifecycleState.LIVE
    m.suspend()     # live → suspended
    assert m.state == LifecycleState.SUSPENDED
    m.resume()      # suspended → warm
    assert m.state == LifecycleState.WARM
    m.activate()    # warm → live
    m.terminate()   # live → terminated
    assert m.state == LifecycleState.TERMINATED


def test_cold_to_live_is_illegal():
    m = LifecycleMachine("agent_1")
    with pytest.raises(LifecycleTransitionError):
        m.transition(LifecycleState.LIVE, "skip_warm")


def test_terminated_is_terminal():
    m = LifecycleMachine("agent_1")
    m.terminate()
    with pytest.raises(LifecycleTransitionError):
        m.transition(LifecycleState.WARM, "resurrect")


def test_events_emitted_on_every_transition():
    m = LifecycleMachine("agent_1")
    events_received = []
    m.subscribe(events_received.append)

    m.load()
    m.activate()
    m.suspend()
    m.terminate()

    assert len(events_received) == 4
    assert events_received[0].from_state == LifecycleState.COLD
    assert events_received[0].to_state == LifecycleState.WARM
    assert events_received[-1].to_state == LifecycleState.TERMINATED


def test_subscriber_exception_does_not_crash_machine():
    """Per spec: subscribers MUST NOT crash the state machine."""
    m = LifecycleMachine("agent_1")

    def bad_cb(event):
        raise RuntimeError("boom")

    m.subscribe(bad_cb)
    m.subscribe(lambda e: None)  # this should still get called

    received = []
    m.subscribe(received.append)

    m.load()
    assert m.state == LifecycleState.WARM
    assert len(received) == 1
