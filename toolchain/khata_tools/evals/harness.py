"""Parser evaluation harness.

The headline number for a project like this is not overall accuracy — it is the
false-booking rate. Every message in the corpus that is *not* a transaction
(an OTP, a balance enquiry, a declined payment, a mandate notice) contains an
amount, and booking any one of them silently corrupts someone's budget in a way
they will not notice until they wonder where their money went.

So the harness reports two gates separately:

  * false bookings — non-transactions that were booked. Must be zero.
  * missed bookings — real transactions that were skipped. Costs coverage,
    but is visible to the user and recoverable.

They are not symmetric and averaging them into one accuracy figure would hide
the one that matters.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable

from ..engine import Engine, ParseResult

# Fields the corpus may assert on. Anything else in an `expect` block is a typo
# and is reported rather than silently ignored.
SCORED_FIELDS = (
    "bookable",
    "kind",
    "amount_minor",
    "direction",
    "channel",
    "merchant_key",
    "account_hint",
    "reference",
    "category",
)


@dataclass(frozen=True)
class Case:
    id: str
    text: str
    expect: dict[str, Any]
    note: str = ""

    def unknown_fields(self) -> list[str]:
        return sorted(k for k in self.expect if k not in SCORED_FIELDS)


@dataclass
class FieldScore:
    checked: int = 0
    correct: int = 0

    @property
    def accuracy(self) -> float:
        return self.correct / self.checked if self.checked else 1.0


@dataclass
class Failure:
    case_id: str
    field: str
    expected: Any
    actual: Any
    text: str

    def __str__(self) -> str:
        return (
            f"{self.case_id}: {self.field} expected {self.expected!r}, got {self.actual!r}\n"
            f"    {self.text[:110]}"
        )


@dataclass
class Report:
    total: int = 0
    fields: dict[str, FieldScore] = field(default_factory=dict)
    failures: list[Failure] = field(default_factory=list)
    false_bookings: list[Failure] = field(default_factory=list)
    missed_bookings: list[Failure] = field(default_factory=list)
    config_errors: list[str] = field(default_factory=list)

    @property
    def cases_fully_correct(self) -> int:
        bad = {f.case_id for f in self.failures}
        return self.total - len(bad)

    @property
    def case_accuracy(self) -> float:
        return self.cases_fully_correct / self.total if self.total else 1.0

    def to_dict(self) -> dict[str, Any]:
        return {
            "total_cases": self.total,
            "cases_fully_correct": self.cases_fully_correct,
            "case_accuracy": round(self.case_accuracy, 4),
            "false_bookings": len(self.false_bookings),
            "missed_bookings": len(self.missed_bookings),
            "field_accuracy": {
                name: {
                    "checked": s.checked,
                    "correct": s.correct,
                    "accuracy": round(s.accuracy, 4),
                }
                for name, s in sorted(self.fields.items())
            },
            "failures": [
                {"case": f.case_id, "field": f.field, "expected": f.expected, "actual": f.actual}
                for f in self.failures
            ],
            "config_errors": self.config_errors,
        }

    def render(self) -> str:
        lines = [
            "khata parser evaluation",
            "=" * 52,
            f"cases              {self.total}",
            f"fully correct      {self.cases_fully_correct}  ({self.case_accuracy:.1%})",
            "",
            f"false bookings     {len(self.false_bookings)}   <- must be 0",
            f"missed bookings    {len(self.missed_bookings)}",
            "",
            "per-field accuracy",
        ]
        for name, s in sorted(self.fields.items()):
            if not s.checked:
                continue
            bar = "#" * int(s.accuracy * 20)
            lines.append(f"  {name:<14} {s.correct:>3}/{s.checked:<3} {s.accuracy:6.1%}  {bar}")

        if self.false_bookings:
            lines += ["", "FALSE BOOKINGS (a non-transaction was recorded as spending)"]
            lines += [f"  {f}" for f in self.false_bookings]
        if self.missed_bookings:
            lines += ["", "MISSED BOOKINGS (a real transaction was skipped)"]
            lines += [f"  {f}" for f in self.missed_bookings]

        other = [f for f in self.failures if f not in self.false_bookings and f not in self.missed_bookings]
        if other:
            lines += ["", "FIELD MISMATCHES"]
            lines += [f"  {f}" for f in other]
        if self.config_errors:
            lines += ["", "CORPUS PROBLEMS"] + [f"  {e}" for e in self.config_errors]
        return "\n".join(lines)


def load_corpus(path: str | Path) -> list[Case]:
    """Load a JSONL corpus. Blank lines and `#` comments are allowed."""
    cases: list[Case] = []
    seen: set[str] = set()
    for lineno, raw in enumerate(Path(path).read_text(encoding="utf-8").splitlines(), start=1):
        raw = raw.strip()
        if not raw or raw.startswith("#"):
            continue
        try:
            obj = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ValueError(f"{path}:{lineno}: {exc}") from exc
        case_id = obj.get("id") or f"line{lineno}"
        if case_id in seen:
            raise ValueError(f"{path}:{lineno}: duplicate case id {case_id!r}")
        seen.add(case_id)
        cases.append(Case(id=case_id, text=obj["text"], expect=obj.get("expect", {}), note=obj.get("note", "")))
    return cases


def evaluate(cases: Iterable[Case], engine: Engine | None = None) -> Report:
    cases = list(cases)
    engine = engine or Engine()
    results = engine.parse_batch(c.text for c in cases)

    report = Report(total=len(cases))
    if len(results) != len(cases):
        report.config_errors.append(
            f"engine returned {len(results)} results for {len(cases)} cases; "
            "a message probably contained a newline"
        )
        return report

    for case, result in zip(cases, results):
        for bad in case.unknown_fields():
            report.config_errors.append(f"{case.id}: unknown expect field {bad!r}")
        _score_case(case, result, report)
    return report


def _score_case(case: Case, result: ParseResult, report: Report) -> None:
    expected_bookable = case.expect.get("bookable")

    for name in SCORED_FIELDS:
        if name not in case.expect:
            continue
        # Only score extraction fields when the message was supposed to book
        # and did; otherwise a single wrong booking decision would cascade into
        # a dozen misleading field failures.
        if name not in ("bookable", "kind") and not (expected_bookable and result.bookable):
            continue

        want = case.expect[name]
        got = getattr(result, name)
        score = report.fields.setdefault(name, FieldScore())
        score.checked += 1
        if got == want:
            score.correct += 1
            continue

        failure = Failure(case.id, name, want, got, case.text)
        report.failures.append(failure)
        if name == "bookable":
            (report.false_bookings if got else report.missed_bookings).append(failure)
