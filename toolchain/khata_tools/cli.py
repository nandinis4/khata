"""Command line entry point for the khata toolchain."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .engine import Engine, EngineNotFound
from .evals import evaluate, load_corpus
from .report import build_report, read_ledger
from .rulesynth import get_provider, load_corrections, synthesize, write_pack

DEFAULT_CORPUS = Path(__file__).resolve().parent.parent / "evals" / "corpus.jsonl"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="khata-tools",
        description="Offline tooling for khata: evaluate the parser, compile corrections into "
        "rules, and build a monthly report. None of this runs on the phone.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_eval = sub.add_parser("eval", help="measure the parser against a labelled corpus")
    p_eval.add_argument("--corpus", type=Path, default=DEFAULT_CORPUS)
    p_eval.add_argument("--binary", help="path to a prebuilt khata binary (default: go run)")
    p_eval.add_argument("--json", action="store_true", help="emit machine-readable results")
    p_eval.add_argument(
        "--min-accuracy",
        type=float,
        default=0.0,
        help="exit non-zero below this case accuracy; use in CI as a regression gate",
    )
    p_eval.add_argument(
        "--allow-false-bookings",
        action="store_true",
        help="do not fail when a non-transaction was booked (you almost certainly do not want this)",
    )

    p_syn = sub.add_parser("synth", help="compile corrections into a verified rule pack")
    p_syn.add_argument("corrections", type=Path, help="JSON from `khata export`")
    p_syn.add_argument("-o", "--out", type=Path, default=Path("synthesized.json"))
    p_syn.add_argument("--provider", default="heuristic", choices=["heuristic", "ollama", "anthropic"])
    p_syn.add_argument("--min-support", type=int, default=1)
    p_syn.add_argument("--dry-run", action="store_true", help="print what would be written, write nothing")

    p_rep = sub.add_parser("report", help="build a monthly report from the ledger file")
    p_rep.add_argument("ledger", type=Path)
    p_rep.add_argument("--period", help="YYYY-MM (default: the most recent month with spending)")
    p_rep.add_argument("-o", "--out", type=Path, help="write to a file instead of stdout")

    args = parser.parse_args(argv)

    match args.command:
        case "eval":
            return _run_eval(args)
        case "synth":
            return _run_synth(args)
        case "report":
            return _run_report(args)
    return 2


def _run_eval(args) -> int:
    cases = load_corpus(args.corpus)
    try:
        report = evaluate(cases, Engine(binary=args.binary))
    except EngineNotFound as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    if args.json:
        print(json.dumps(report.to_dict(), indent=2))
    else:
        print(report.render())

    failed = False
    if report.false_bookings and not args.allow_false_bookings:
        print(
            f"\nFAIL: {len(report.false_bookings)} non-transaction(s) were booked as spending.",
            file=sys.stderr,
        )
        failed = True
    if report.case_accuracy < args.min_accuracy:
        print(
            f"\nFAIL: case accuracy {report.case_accuracy:.1%} is below the "
            f"{args.min_accuracy:.1%} gate.",
            file=sys.stderr,
        )
        failed = True
    if report.config_errors:
        print(f"\nFAIL: the corpus itself has {len(report.config_errors)} problem(s).", file=sys.stderr)
        failed = True
    return 1 if failed else 0


def _run_synth(args) -> int:
    corrections = load_corrections(args.corrections)
    if not corrections:
        print("no corrections to compile; categorise a few transactions first", file=sys.stderr)
        return 0

    result = synthesize(corrections, provider=get_provider(args.provider), min_support=args.min_support)
    print(result.render())

    if args.dry_run:
        print("\n--dry-run: nothing written")
        print(json.dumps(result.pack(), indent=2))
        return 0

    path = write_pack(result, args.out)
    print(f"\nwrote {len(result.rules)} rule(s) to {path}")
    print(f"load it with:  khata -pack {path} month")
    return 0


def _run_report(args) -> int:
    txns = read_ledger(args.ledger)
    text = build_report(txns, period=args.period)
    if args.out:
        args.out.write_text(text, encoding="utf-8")
        print(f"wrote {args.out}")
    else:
        print(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
