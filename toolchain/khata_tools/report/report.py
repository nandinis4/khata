"""Monthly insight report, generated from the ledger file directly.

The ledger is newline-delimited JSON, so this module reads it with the standard
library and no help from the Go side. That is the practical payoff of the
storage format: a user's own data is available to their own tools without an
export step or a schema dump.

Nothing here phones home, and nothing here needs a model. The interesting
observations in personal spending are arithmetic — what recurs, what is
creeping up, what the small purchases add up to — and arithmetic is checkable.
"""

from __future__ import annotations

import json
from collections import defaultdict
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from statistics import median
from typing import Any, Iterable

SMALL_SPEND_MINOR = 20000  # ₹200: the threshold below which people stop noticing


@dataclass(frozen=True)
class Txn:
    id: str
    occurred_at: datetime
    amount_minor: int
    direction: str
    category: str
    merchant_key: str
    merchant_raw: str

    @property
    def period(self) -> str:
        return self.occurred_at.strftime("%Y-%m")

    @property
    def is_spend(self) -> bool:
        return self.direction == "debit" and self.category not in ("transfers", "income")


def read_ledger(path: str | Path) -> list[Txn]:
    """Replay the append-only log into the current set of transactions.

    Category events are applied in order, so a corrected transaction reports the
    category the user chose rather than the one the engine guessed.
    """
    txns: dict[str, dict[str, Any]] = {}

    for line in Path(path).read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            # A torn final line is expected after an interrupted write; the Go
            # side repairs it on next open. Stop cleanly rather than fail.
            break

        kind = ev.get("type")
        if kind == "txn" and ev.get("txn"):
            t = ev["txn"]
            txns[t["id"]] = t
        elif kind == "category" and ev.get("category"):
            c = ev["category"]
            if c["transaction_id"] in txns:
                txns[c["transaction_id"]]["category"] = c["to"]

    out = []
    for t in txns.values():
        out.append(
            Txn(
                id=t["id"],
                occurred_at=_parse_time(t["occurred_at"]),
                amount_minor=int(t["amount_minor"]),
                direction=t.get("direction", ""),
                category=t.get("category", "uncategorized"),
                merchant_key=t.get("merchant_key", ""),
                merchant_raw=t.get("merchant_raw", ""),
            )
        )
    return sorted(out, key=lambda t: t.occurred_at)


def _parse_time(value: Any) -> datetime:
    if isinstance(value, (int, float)):
        return datetime.fromtimestamp(value, tz=timezone.utc)
    text = str(value).replace("Z", "+00:00")
    return datetime.fromisoformat(text)


def money(minor: int) -> str:
    """Format minor units with Indian digit grouping, matching the Go side."""
    neg, minor = minor < 0, abs(minor)
    whole, frac = divmod(minor, 100)
    s = str(whole)
    if len(s) > 3:
        head, tail = s[:-3], s[-3:]
        parts = []
        while len(head) > 2:
            parts.insert(0, head[-2:])
            head = head[:-2]
        if head:
            parts.insert(0, head)
        s = ",".join(parts) + "," + tail
    if frac:
        s = f"{s}.{frac:02d}"
    return ("-" if neg else "") + s


def find_recurring(txns: Iterable[Txn], min_occurrences: int = 3) -> list[dict[str, Any]]:
    """Spot merchants charging a steady amount at a steady cadence.

    Subscriptions are the spending people most reliably forget about, and they
    are also the easiest to detect: same counterparty, near-identical amount,
    roughly monthly gaps. The amount tolerance is generous because plan prices
    change and taxes move.
    """
    by_merchant: dict[str, list[Txn]] = defaultdict(list)
    for t in txns:
        if t.is_spend and t.merchant_key:
            by_merchant[t.merchant_key].append(t)

    found = []
    for key, group in by_merchant.items():
        if len(group) < min_occurrences:
            continue
        group.sort(key=lambda t: t.occurred_at)
        gaps = [
            (b.occurred_at - a.occurred_at).days
            for a, b in zip(group, group[1:])
        ]
        if not gaps:
            continue
        typical_gap = median(gaps)
        if not (24 <= typical_gap <= 38):
            continue

        amounts = [t.amount_minor for t in group]
        typical = median(amounts)
        if typical <= 0:
            continue
        if max(abs(a - typical) for a in amounts) / typical > 0.20:
            continue

        found.append(
            {
                "merchant": group[-1].merchant_raw or key,
                "amount_minor": int(typical),
                "occurrences": len(group),
                "median_gap_days": int(typical_gap),
                "annualised_minor": int(typical) * 12,
                "category": group[-1].category,
            }
        )
    return sorted(found, key=lambda r: -r["annualised_minor"])


def build_report(txns: list[Txn], period: str | None = None) -> str:
    spends = [t for t in txns if t.is_spend]
    if not spends:
        return "# khata report\n\nNo spending recorded yet.\n"

    periods = sorted({t.period for t in spends})
    period = period or periods[-1]
    current = [t for t in spends if t.period == period]
    if not current:
        return f"# khata report — {period}\n\nNo spending recorded in this period.\n"

    prev_period = periods[periods.index(period) - 1] if period in periods and periods.index(period) > 0 else None
    previous = [t for t in spends if prev_period and t.period == prev_period]

    total = sum(t.amount_minor for t in current)
    income = sum(t.amount_minor for t in txns if t.period == period and t.category == "income")

    by_cat: dict[str, int] = defaultdict(int)
    for t in current:
        by_cat[t.category] += t.amount_minor
    prev_by_cat: dict[str, int] = defaultdict(int)
    for t in previous:
        prev_by_cat[t.category] += t.amount_minor

    by_merchant: dict[str, int] = defaultdict(int)
    merchant_label: dict[str, str] = {}
    for t in current:
        k = t.merchant_key or "(unknown)"
        by_merchant[k] += t.amount_minor
        merchant_label.setdefault(k, t.merchant_raw or k)

    lines = [
        f"# khata report — {_pretty_period(period)}",
        "",
        f"**Total spend** ₹{money(total)} across {len(current)} transactions.",
    ]
    if income:
        rate = (income - total) / income * 100
        lines.append(f"**Income** ₹{money(income)}, leaving a savings rate of {rate:.0f}%.")
    if previous:
        delta = total - sum(t.amount_minor for t in previous)
        direction = "more" if delta > 0 else "less"
        lines.append(f"That is ₹{money(abs(delta))} {direction} than {_pretty_period(prev_period)}.")

    lines += ["", "## Where it went", "", "| Category | Spent | Share | vs last month |", "|---|---:|---:|---:|"]
    for cat, amt in sorted(by_cat.items(), key=lambda kv: -kv[1]):
        share = amt / total * 100
        if prev_by_cat.get(cat):
            d = amt - prev_by_cat[cat]
            change = f"{'+' if d >= 0 else '−'}₹{money(abs(d))}"
        elif previous:
            change = "new"
        else:
            change = "—"
        lines.append(f"| {cat.replace('_', ' ')} | ₹{money(amt)} | {share:.0f}% | {change} |")

    lines += ["", "## Biggest counterparties", ""]
    for key, amt in sorted(by_merchant.items(), key=lambda kv: -kv[1])[:8]:
        lines.append(f"- **{merchant_label[key]}** — ₹{money(amt)}")

    small = [t for t in current if t.amount_minor <= SMALL_SPEND_MINOR]
    if small:
        small_total = sum(t.amount_minor for t in small)
        lines += [
            "",
            "## The small ones",
            "",
            f"{len(small)} transactions of ₹{money(SMALL_SPEND_MINOR)} or less came to "
            f"**₹{money(small_total)}** — {small_total / total * 100:.0f}% of the month. "
            "Individually forgettable, collectively not.",
        ]

    recurring = find_recurring(spends)
    if recurring:
        lines += ["", "## Looks recurring", "", "| Merchant | Each | Seen | Annualised |", "|---|---:|---:|---:|"]
        for r in recurring:
            lines.append(
                f"| {r['merchant']} | ₹{money(r['amount_minor'])} | {r['occurrences']}× | "
                f"₹{money(r['annualised_minor'])} |"
            )
        lines.append("")
        lines.append(
            "_Detected by cadence and amount stability, not by any subscription list. "
            "Worth checking you still use all of these._"
        )

    unc = by_cat.get("uncategorized", 0)
    if unc:
        lines += [
            "",
            "## Needs a decision",
            "",
            f"₹{money(unc)} is still uncategorised. Run `khata review` to file it — "
            "each correction becomes a rule, so the same merchant will not ask twice.",
        ]

    lines += ["", "---", "", "_Generated locally by khata-tools. This report was built from your ledger file and sent nowhere._"]
    return "\n".join(lines) + "\n"


def _pretty_period(period: str | None) -> str:
    if not period:
        return "—"
    return datetime.strptime(period, "%Y-%m").strftime("%B %Y")
