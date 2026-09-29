"""Loomwork CLI — `loomwork` entrypoint.

Commands:
    loomwork aci build <source_dir> -o <output.aci>     Build an ACI from a source dir
    loomwork aci verify <aci_file>                      Verify signature + digests + SLSA
    loomwork aci inspect <aci_file>                     Show manifest + file listing
    loomwork aci conformance <aci_file>                 Run C1-C11 conformance checks
    loomwork aci sign <source_dir> --key <pem>          Sign an ACI in-place
    loomwork aci keygen --out <pem>                     Generate a new Ed25519 signing key

    loomwork runtime start [--port 7878]                Start mesh runtime HTTP/WS server
    loomwork runtime load <aci_file>                    Load an ACI into a running runtime
    loomwork runtime list                                List loaded agents
    loomwork runtime terminate <agent_id>              Terminate an agent

    loomwork agent delegate --to <url> --skill <s>     Delegate a task via AMP
    loomwork agent hello --to <url>                    Run handshake with a peer
"""
from __future__ import annotations

import asyncio
import json
import sys
from pathlib import Path
from typing import Optional

import click
from rich.console import Console
from rich.table import Table
from rich.json import JSON as RichJSON

from loomwork import __version__
from loomwork.aci.archive import AciArchive, pack_aci, unpack_aci
from loomwork.aci.manifest import Manifest
from loomwork.aci.signing import (
    SigningKey, VerifyingKey, sign_aci, verify_aci_signature,
)
from loomwork.aci.slsa import build_slsa_attestation, verify_slsa
from loomwork.aci.conformance import ConformanceChecker

console = Console()
err_console = Console(stderr=True)


@click.group()
@click.version_option(__version__)
def main():
    """Loomwork — open operating system for portable personal agents."""
    pass


# ── ACI commands ─────────────────────────────────────────────────────────────
@main.group()
def aci():
    """Agent Container Image commands."""
    pass


@aci.command("build")
@click.argument("source_dir", type=click.Path(exists=True, file_okay=False))
@click.option("-o", "--output", required=True, help="Output .aci file path")
@click.option("--signing-key", type=click.Path(exists=True), help="PEM file with Ed25519 private key")
@click.option("--with-slsa", is_flag=True, default=False,
              help="Generate SLSA attestation as a sidecar .slsa.json file")
def aci_build(source_dir: str, output: str, signing_key: Optional[str], with_slsa: bool):
    """Build an ACI from a source directory.

    Pipeline: sign (in-place) → pack → if --with-slsa, generate attestation
    as a sidecar file (next to the .aci file). The SLSA attestation is a
    separate artifact (per Sigstore/SLSA convention) — its subject is the
    packed .aci archive's digest.
    """
    # Sign first (in-place) if requested
    if signing_key:
        key_pem = Path(signing_key).read_bytes()
        sk = SigningKey.from_pem(key_pem)
        sign_aci(source_dir, sk)
        console.print(f"[green]✓[/] Signed {source_dir}/signatures/manifest.sig")

    # Pack
    archive = AciArchive.from_directory(source_dir)
    archive_bytes = archive.to_tar_gz()
    out = Path(output)
    out.write_bytes(archive_bytes)
    size_kb = out.stat().st_size / 1024
    console.print(f"[green]✓[/] Built ACI: {out} ({size_kb:.1f} KB)")

    if with_slsa:
        attestation = build_slsa_attestation(archive.manifest, archive_bytes, source_repo=source_dir)
        slsa_sidecar = out.with_suffix(".slsa.json")
        slsa_sidecar.write_bytes(attestation.to_json())
        console.print(f"[green]✓[/] Wrote SLSA sidecar: {slsa_sidecar}")


@aci.command("verify")
@click.argument("aci_file", type=click.Path(exists=True))
@click.option("--trust-unsigned", is_flag=True, help="Allow ACIs without signature")
def aci_verify(aci_file: str, trust_unsigned: bool):
    """Verify an ACI's signature + digests + SLSA attestation."""
    import tempfile
    data = Path(aci_file).read_bytes()
    try:
        archive = AciArchive.from_tar_gz(data)
    except Exception as e:
        err_console.print(f"[red]✗[/] Archive parse failed: {e}")
        sys.exit(1)

    # Verify digests (already done by from_tar_gz, but report)
    console.print(f"[green]✓[/] All {len(archive.manifest.digests)} file digests verified")

    # Verify signature
    with tempfile.TemporaryDirectory() as tmp:
        unpack_aci(aci_file, tmp)
        sig_ok = verify_aci_signature(tmp)
        has_sig = (Path(tmp) / "signatures" / "manifest.sig").exists()

        if not has_sig:
            if trust_unsigned:
                console.print("[yellow]⚠[/] No signature (trust-unsigned mode)")
            else:
                err_console.print("[red]✗[/] No signature present")
                sys.exit(1)
        elif sig_ok:
            console.print("[green]✓[/] Manifest signature valid")
        else:
            err_console.print("[red]✗[/] Manifest signature INVALID")
            sys.exit(1)

        # Verify SLSA (sidecar file next to the .aci)
        slsa_sidecar = Path(aci_file).with_suffix(".slsa.json")
        if slsa_sidecar.exists():
            import hashlib
            att = __import__("loomwork.aci.slsa", fromlist=["SlsaAttestation"]).SlsaAttestation.from_json(slsa_sidecar.read_bytes())
            expected = f"sha256:{hashlib.sha256(data).hexdigest()}"
            if verify_slsa(att, expected):
                console.print("[green]✓[/] SLSA attestation valid")
            else:
                err_console.print("[yellow]⚠[/] SLSA attestation invalid")
        else:
            console.print("[yellow]⚠[/] No SLSA attestation (sidecar file not found)")

    console.print(f"\n[bold]ACI verified:[/] {archive.manifest.metadata.name}@{archive.manifest.metadata.version}")


@aci.command("inspect")
@click.argument("aci_file", type=click.Path(exists=True))
def aci_inspect(aci_file: str):
    """Show manifest + file listing for an ACI."""
    data = Path(aci_file).read_bytes()
    archive = AciArchive.from_tar_gz(data)

    # Manifest
    console.print("\n[bold]Manifest:[/]")
    console.print_json(archive.manifest.to_json().decode("utf-8"))

    # File listing
    table = Table(title="Files")
    table.add_column("Path", style="cyan")
    table.add_column("Size", justify="right", style="magenta")
    for path, content in sorted(archive.files.items()):
        if path == "manifest.json":
            continue
        table.add_row(path, f"{len(content)} B")
    for path, content in sorted(archive.signatures.items()):
        table.add_row(f"[dim]{path}[/]", f"{len(content)} B")
    console.print(table)


@aci.command("conformance")
@click.argument("aci_file", type=click.Path(exists=True))
def aci_conformance(aci_file: str):
    """Run C1-C11 conformance checks on an ACI."""
    checker = ConformanceChecker()
    result = checker.check_aci(aci_file)

    table = Table(title=f"Conformance — {Path(aci_file).name}")
    table.add_column("#", style="cyan")
    table.add_column("Requirement", style="white")
    table.add_column("Level", style="yellow")
    table.add_column("Pass", justify="center")
    table.add_column("Message", style="dim")
    for cid, req, level, ok, msg in result.checks:
        status = "[green]✓[/]" if ok else ("[yellow]-[/]" if "N/A" in msg else "[red]✗[/]")
        table.add_row(cid, req[:60] + ("…" if len(req) > 60 else ""), level, status, msg)
    console.print(table)
    console.print(f"\n[bold]{result.summary()}[/]")
    if not result.passed:
        sys.exit(1)


@aci.command("sign")
@click.argument("source_dir", type=click.Path(exists=True, file_okay=False))
@click.option("--key", "key_path", required=True, type=click.Path(exists=True),
              help="PEM file with Ed25519 private key")
def aci_sign(source_dir: str, key_path: str):
    """Sign an ACI in-place (writes signatures/manifest.{sig,cert})."""
    sk = SigningKey.from_pem(Path(key_path).read_bytes())
    sig_path = sign_aci(source_dir, sk)
    console.print(f"[green]✓[/] Signed: {sig_path}")
    console.print(f"  Key ID: {sk.key_id}")


@aci.command("keygen")
@click.option("--out", "out_path", required=True, type=click.Path(),
              help="Output PEM file path")
@click.option("--password", help="Password to encrypt the key (optional)")
def aci_keygen(out_path: str, password: Optional[str]):
    """Generate a new Ed25519 signing key."""
    sk = SigningKey.generate()
    pw = password.encode() if password else None
    Path(out_path).write_bytes(sk.to_pem(pw))
    console.print(f"[green]✓[/] Generated Ed25519 key: {out_path}")
    console.print(f"  Key ID: {sk.key_id}")
    console.print(f"  Public key: {sk.verifying_key().to_pem().decode()}")


# ── Runtime commands ─────────────────────────────────────────────────────────
@main.group()
def runtime():
    """Mesh runtime commands."""
    pass


@runtime.command("start")
@click.option("--port", default=7878, help="WebSocket port")
@click.option("--host", default="127.0.0.1", help="Bind host")
@click.option("--memory-db", default="~/.loomwork/memory.db", help="Memory layer DB path")
@click.option("--trust-unsigned", is_flag=True, help="Allow ACIs without signature")
def runtime_start(port: int, host: str, memory_db: str, trust_unsigned: bool):
    """Start the mesh runtime as a WebSocket server."""
    from loomwork.runtime.orchestrator import Orchestrator
    from loomwork.runtime.memory import MemoryLayer
    from loomwork.amp.transports import WebSocketTransport

    db_path = Path(memory_db).expanduser()
    memory = MemoryLayer(db_path, passphrase=None)  # no encryption in CLI demo
    orchestrator = Orchestrator(memory=memory, trust_unsigned=trust_unsigned)

    async def handler(transport: WebSocketTransport):
        from loomwork.amp.rpcs import AmpServer, default_delegate_handler

        server = AmpServer(task_graph=orchestrator.task_graph)
        server.register("amp/delegate",
                        lambda p, e: default_delegate_handler(p, e, orchestrator.task_graph))
        server.register("amp/hello", _make_hello_handler(orchestrator))
        server.register("amp/accept", _make_accept_handler(orchestrator))
        await server.serve(transport.send, _arecv(transport))

    console.print(f"[bold green]Loomwork runtime[/] v{__version__}")
    console.print(f"  Listening on ws://{host}:{port}")
    console.print(f"  Memory DB: {db_path}")
    console.print(f"  Trust unsigned: {trust_unsigned}")
    console.print("  Press Ctrl-C to stop\n")

    asyncio.run(WebSocketTransport.serve(host, port, handler))


async def _arecv(transport):
    async for env in transport.recv():
        return env
    raise RuntimeError("transport closed")


def _make_hello_handler(orchestrator):
    from loomwork.amp.handshake import CapabilitiesResult
    async def handler(params, envelope):
        caps = CapabilitiesResult(
            acceptedSkills=["delegate", "report"],
            offeredSkills=["research", "summarize"],
            costModel={"currency": "loompoint", "per1kTokens": 10},
        )
        return caps.to_dict()
    return handler


def _make_accept_handler(orchestrator):
    async def handler(params, envelope):
        return {"accepted": True}
    return handler


@runtime.command("load")
@click.argument("aci_file", type=click.Path(exists=True))
@click.option("--runtime-url", default="ws://127.0.0.1:7878",
              help="Running runtime WebSocket URL")
def runtime_load(aci_file: str, runtime_url: str):
    """Load an ACI into a running runtime (via subprocess import for now)."""
    # For simplicity, this loads directly into a fresh in-process orchestrator
    # Real production: HTTP API call to the running runtime
    from loomwork.runtime.orchestrator import Orchestrator
    from loomwork.runtime.memory import MemoryLayer

    memory = MemoryLayer(Path("~/.loomwork/memory.db").expanduser(), passphrase=None)
    orch = Orchestrator(memory=memory, trust_unsigned=True)
    instance = orch.load_aci(aci_file)
    console.print(f"[green]✓[/] Loaded ACI")
    console.print(f"  Agent ID: {instance.agent_id}")
    console.print(f"  Name:     {instance.name}")
    console.print(f"  Version:  {instance.version}")
    console.print(f"  State:    {instance.state.value}")


@runtime.command("list")
def runtime_list():
    """List loaded agents (in-process only — real impl hits the runtime API)."""
    console.print("[dim]Note: runtime list operates in-process. For a running runtime, use the HTTP API.[/]")
    from loomwork.runtime.orchestrator import Orchestrator
    orch = Orchestrator()
    agents = orch.list_agents()
    if not agents:
        console.print("[yellow]No agents loaded[/]")
        return
    table = Table(title="Loaded agents")
    table.add_column("Agent ID", style="cyan")
    table.add_column("Name", style="white")
    table.add_column("Version", style="magenta")
    table.add_column("State", style="green")
    for a in agents:
        table.add_row(a.agent_id, a.name, a.version, a.state.value)
    console.print(table)


# ── Agent commands ───────────────────────────────────────────────────────────
@main.group()
def agent():
    """Agent interaction commands."""
    pass


@agent.command("delegate")
@click.option("--to", "peer_url", required=True, help="Peer WebSocket URL")
@click.option("--skill", required=True, help="Skill to invoke")
@click.option("--input", "input_json", required=True, help="JSON input")
@click.option("--intent", default="research", help="Task intent")
@click.option("--budget-tokens", default=8000, help="Token budget")
def agent_delegate(peer_url: str, skill: str, input_json: str, intent: str, budget_tokens: int):
    """Delegate a task to a peer agent via AMP."""
    import json
    from loomwork.amp.transports import WebSocketTransport
    from loomwork.amp.rpcs import AmpClient, DelegateParams
    from loomwork.amp.handshake import run_handshake_initiator

    async def run():
        transport = await WebSocketTransport.connect(peer_url)
        try:
            # Handshake
            session_id, caps, accept = await run_handshake_initiator(
                transport.send, _arecv(transport),
                my_agent_id="did:key:ed25519:cli",
                my_offered_skills=["delegate", "report"],
            )
            console.print(f"[green]✓[/] Handshake complete. Session: {session_id}")
            console.print(f"  Peer offers: {caps.offeredSkills}")

            # Delegate
            client = AmpClient(transport.send, _arecv(transport),
                               session_id=session_id)
            params = DelegateParams(
                spec={
                    "intent": intent,
                    "inputs": json.loads(input_json),
                    "expectedOutputs": [f"{skill}:string"],
                },
                budget={"maxTokens": budget_tokens, "maxToolCalls": 5},
            )
            result = await client.delegate(params)
            console.print(f"[green]✓[/] Delegated: {result}")
        finally:
            await transport.close()

    asyncio.run(run())


@agent.command("hello")
@click.option("--to", "peer_url", required=True, help="Peer WebSocket URL")
def agent_hello(peer_url: str):
    """Run an AMP handshake with a peer (no further RPCs)."""
    from loomwork.amp.transports import WebSocketTransport
    from loomwork.amp.handshake import run_handshake_initiator

    async def run():
        transport = await WebSocketTransport.connect(peer_url)
        try:
            session_id, caps, accept = await run_handshake_initiator(
                transport.send, _arecv(transport),
                my_agent_id="did:key:ed25519:cli",
                my_offered_skills=["ping"],
            )
            console.print(f"[green]✓[/] Handshake OK. Session: {session_id}")
            console.print(f"  Peer accepted: {caps.acceptedSkills}")
            console.print(f"  Peer declined: {caps.declinedSkills}")
            console.print(f"  Peer offers:   {caps.offeredSkills}")
            console.print(f"  Cost model:    {caps.costModel}")
        finally:
            await transport.close()

    asyncio.run(run())


if __name__ == "__main__":
    main()
