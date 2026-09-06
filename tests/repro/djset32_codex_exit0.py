#!/usr/bin/env python3
"""h2ops-djset.32 — executable reproduction for: Codex children exit 0 clean.

SIGNATURE UNDER TEST
    A Codex child launched by h2 records one turn_completed with non-zero
    input tokens and ZERO output tokens, never answers the h2 message that
    was waiting for it, and exits with status 0 after 20-35 seconds.

WHY THIS FILE EXISTS BEFORE ANY FIX
    An agent that dies 25s after being told something is indistinguishable
    from an agent that ignored you. A fix without a witness cannot be told
    apart from the intermittency it claims to have removed. So the witness
    is written first and it is cheap: each arm ends in either 124 (the child
    was still alive when the timeout killed it) or 0 (the child quit on its
    own, which is the defect).

    exit 124  -> SURVIVED. This arm is not the cause.
    exit 0    -> REPRODUCED. The child quit clean on its own.
    anything else -> a THIRD thing; do not fold it into either bucket.

FIDELITY
    The argv, the working directory and the environment are not transcribed
    by hand. All three are extracted from
    `h2 run codex-coder --role codex-coder --dry-run`, which renders the
    exact child command h2 would launch, so this check keeps testing what h2
    actually does even after the role file changes. The only substitution is
    the OTEL port placeholder, which is pointed at a dead port — concierge
    measured that arg innocent on 2026-09-06 (survived 70s, exit 124) and it
    is kept only so the argv stays faithful.

ARMS
    B  control       full argv MINUS `-c instructions=...`
    A  suspect (b)   full argv, including `-c instructions=...`

    Concierge hand-tested the equivalent of B on 2026-09-06 and it survived.
    B is re-run here anyway, every time, because a one-armed check cannot
    tell "this flag kills the child" from "everything is dying right now",
    and the second reading costs 70 seconds.

NOT COVERED HERE
    Suspect (a), h2's message injection into the TUI, needs bytes written
    into the child's PTY the way h2 writes them. That is a separate arm and
    it is not guessed at in this file.

RUNNER
    python3 tests/repro/djset32_codex_exit0.py
    python3 tests/repro/djset32_codex_exit0.py --arm A     (one arm only)

    On-demand. Not part of `make test`: it launches a real Codex child and
    costs ~70s per surviving arm.
"""

from __future__ import annotations

import argparse
import os
import shlex
import subprocess
import sys
import time

DRY_RUN = ["h2", "run", "codex-coder", "--role", "codex-coder", "--dry-run"]
DEAD_PORT = "59999"  # measured innocent 2026-09-06; kept for argv fidelity
TIMEOUT_S = 70

# The one deliberate deviation from the dry-run, and it is load-bearing.
#
# This check is itself run by an h2 agent, and agent shells carry TERM=dumb.
# Codex refuses the interactive TUI outright under TERM=dumb: exit 1 in under
# a second, with "Refusing to start the interactive TUI". That is a DIFFERENT
# defect — concierge ruled it out for djset.32 on exactly that evidence — and
# if this check let it through, every arm would report exit 1 and the run
# would look like a measurement while measuring nothing.
#
# The first version of this file did let it through. Both arms came back
# exit 1 at 0.2s. That is why the arms report a THIRD OUTCOME loudly instead
# of bucketing everything into "did not reproduce": a check that cannot run
# and a check that found nothing must not look alike.
TERM_FOR_CHILD = "xterm-256color"

SURVIVED = 124
QUIT_CLEAN = 0


class Launch:
    """Everything h2 would hand the child: argv, working dir, environment."""

    def __init__(self, argv: list[str], cwd: str, env: dict[str, str]) -> None:
        self.argv = argv
        self.cwd = cwd
        self.env = env


def _block(lines: list[str], header: str) -> list[str]:
    """Lines under `header:` up to the first blank line.

    A missing header is fatal rather than empty. The first version of this
    check set no environment at all and both arms died in 0.2s for a reason
    that had nothing to do with the defect; silently reproducing that by
    treating an absent block as "no environment" is the same mistake with a
    parser in front of it.
    """
    try:
        start = lines.index(header) + 1
    except ValueError:
        raise SystemExit(
            f"dry-run printed no {header!r} block — h2's output shape changed, "
            f"and this check must not guess at what it is supposed to be "
            f"reproducing faithfully"
        )
    out: list[str] = []
    for line in lines[start:]:
        if line.strip() == "":
            break
        out.append(line)
    return out


def extract_launch() -> Launch:
    """Pull argv, cwd and env out of h2's own dry-run rendering.

    The command block looks like:

        Command:
        codex \\
          -c 'otel.exporter={...<PORT>...}' \\
          -c 'instructions="..."' \\
          --ask-for-approval never

    Line continuations and shell quoting are h2's; shlex undoes exactly
    those and nothing else.
    """
    out = subprocess.run(
        DRY_RUN, capture_output=True, text=True, check=True
    ).stdout.splitlines()

    parts = [ln.rstrip().removesuffix("\\").strip() for ln in _block(out, "Command:")]
    argv = shlex.split(" ".join(parts))
    if not argv or argv[0] != "codex":
        raise SystemExit(f"expected argv to start with 'codex', got {argv[:1]}")
    argv = [a.replace("<PORT>", DEAD_PORT) for a in argv]

    # "Working Dir:" is one line, not a block.
    cwd = next(
        (ln.split(":", 1)[1].strip() for ln in out if ln.startswith("Working Dir:")),
        "",
    )
    if not cwd:
        raise SystemExit("dry-run printed no 'Working Dir:' line")

    # h2 adds these on top of the inherited environment rather than replacing
    # it, so the child still needs PATH, HOME and the rest to run at all.
    env = dict(os.environ)
    for line in _block(out, "Environment:"):
        key, _, value = line.strip().partition("=")
        env[key] = value
    env["TERM"] = TERM_FOR_CHILD
    return Launch(argv, cwd, env)


def strip_instructions(argv: list[str]) -> list[str]:
    """Drop the `-c instructions=...` pair, leaving every other flag alone."""
    out: list[str] = []
    i = 0
    dropped = 0
    while i < len(argv):
        if (
            argv[i] == "-c"
            and i + 1 < len(argv)
            and argv[i + 1].startswith("instructions=")
        ):
            i += 2
            dropped += 1
            continue
        out.append(argv[i])
        i += 1
    if dropped != 1:
        raise SystemExit(
            f"expected exactly one `-c instructions=` pair in the argv, found "
            f"{dropped}. The control arm is only a control if it differs from "
            f"the suspect arm by that one thing."
        )
    return out


def run_arm(name: str, argv: list[str], launch: Launch) -> int:
    """Run one arm under a real PTY and return its exit status.

    `script -qec` is the same PTY wrapper concierge used by hand, kept so
    this result is comparable with that measurement rather than merely
    similar to it. cwd and env come from the dry-run, not from this shell.
    """
    inner = " ".join(shlex.quote(a) for a in argv)
    wrapped = ["timeout", str(TIMEOUT_S), "script", "-qec", inner, "/dev/null"]

    print(f"\n=== arm {name} ===")
    print(f"argv ({len(argv)} words): {inner[:160]}{'...' if len(inner) > 160 else ''}")
    started = time.monotonic()
    proc = subprocess.run(
        wrapped, cwd=launch.cwd, env=launch.env, capture_output=True, text=True
    )
    elapsed = time.monotonic() - started

    print(f"exit={proc.returncode} after {elapsed:.1f}s")
    if proc.returncode == SURVIVED:
        print("  SURVIVED — child was still alive when the timeout killed it")
    elif proc.returncode == QUIT_CLEAN:
        print("  REPRODUCED — child quit on its own with status 0")
    else:
        print("  THIRD OUTCOME — neither survival nor the signature; report as its own thing")

    tail = (proc.stdout or "").strip().splitlines()[-8:]
    if tail:
        print("  last child output:")
        for line in tail:
            print(f"    {line}")
    if proc.stderr.strip():
        print(f"  stderr: {proc.stderr.strip()[:400]}")
    return proc.returncode


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--arm", choices=["A", "B", "both"], default="both")
    args = ap.parse_args()

    launch = extract_launch()
    argv_a = launch.argv
    argv_b = strip_instructions(argv_a)
    print(f"cwd: {launch.cwd}")
    print(f"TERM: {launch.env['TERM']} (overridden; see TERM_FOR_CHILD)")

    results: dict[str, int] = {}
    if args.arm in ("B", "both"):
        results["B (control, no -c instructions)"] = run_arm("B", argv_b, launch)
    if args.arm in ("A", "both"):
        results["A (suspect b, with -c instructions)"] = run_arm("A", argv_a, launch)

    print("\n=== summary ===")
    for label, code in results.items():
        verdict = {SURVIVED: "SURVIVED", QUIT_CLEAN: "REPRODUCED"}.get(code, "THIRD OUTCOME")
        print(f"  {label}: exit {code} — {verdict}")

    # The interesting reading is the DIFFERENCE between the arms, not either
    # arm alone. Say so explicitly rather than leaving it to be inferred.
    if len(results) == 2:
        a = results["A (suspect b, with -c instructions)"]
        b = results["B (control, no -c instructions)"]
        if a == QUIT_CLEAN and b == SURVIVED:
            print("\n  -c instructions=... IS the difference. Suspect (b) confirmed.")
        elif a == b == SURVIVED:
            print("\n  Both arms survived: suspect (b) is NOT the cause, and the")
            print("  child does not die from launch flags alone. What both arms")
            print("  lack is a delivered h2 message — which is suspect (a).")
        elif a == b == QUIT_CLEAN:
            print("\n  Both arms quit clean: the cause is upstream of the")
            print("  instructions flag entirely. Do not attribute it to h2 yet.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
