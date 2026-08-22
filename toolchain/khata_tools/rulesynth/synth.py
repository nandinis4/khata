"""Compile user corrections into a deterministic rule pack.

This is where the language model lives, and the shape of its involvement is the
point of the design. The model never sees a transaction, never runs on the
phone, and never decides a category at runtime. It is handed a category and the
merchant keys a user assigned to it, and asked for a *match expression*. Its
answer is then checked against the user's own corrections and thrown away if it
does not hold.

Concretely, a proposal is rejected unless it:

  * matches every merchant key the user put in that category, and
  * matches none of the keys the user put in a different category.

Both checks run against real data the user produced, so a confident wrong answer
fails the same way a hesitant wrong answer does. What ships is a JSON rule pack:
reviewable in a diff, identical on every device, and fast enough to run inside a
notification callback.
"""

from __future__ import annotations

import json
import re
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import date
from pathlib import Path
from typing import Any, Iterable

from .providers import HeuristicProvider, Provider

# A synthesized rule outranks the builtin pack but stays below anything the user
# writes by hand. It is an inference from their behaviour, not an instruction.
SYNTHESIZED_PRIORITY = 20


@dataclass(frozen=True)
class Correction:
    merchant_key: str
    to_category: str
    from_category: str = ""
    channel: str = ""

    @classmethod
    def from_json(cls, obj: dict) -> "Correction":
        return cls(
            merchant_key=(obj.get("merchant_key") or "").strip().lower(),
            to_category=(obj.get("to_category") or "").strip(),
            from_category=(obj.get("from_category") or "").strip(),
            channel=(obj.get("channel") or "").strip(),
        )


@dataclass
class Rejection:
    category: str
    reason: str
    proposal: dict[str, Any]


@dataclass
class SynthesisResult:
    rules: list[dict[str, Any]] = field(default_factory=list)
    rejections: list[Rejection] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)
    provider: str = "heuristic"

    def pack(self, name: str = "synthesized", version: str | None = None) -> dict[str, Any]:
        return {
            "name": name,
            "version": version or date.today().isoformat(),
            "description": (
                f"Compiled from user corrections by khata-tools using the {self.provider} provider. "
                "Every rule here was verified against the corrections it claims to explain."
            ),
            "rules": self.rules,
        }

    def render(self) -> str:
        lines = [
            f"rule synthesis ({self.provider})",
            "=" * 52,
            f"rules produced   {len(self.rules)}",
            f"proposals rejected {len(self.rejections)}",
            f"groups skipped   {len(self.skipped)}",
        ]
        if self.rules:
            lines += ["", "rules"]
            for r in self.rules:
                m = r["match"]
                expr = ", ".join(m.get("merchant_contains", [])) or m.get("merchant_regex", "")
                lines.append(f"  {r['category']:<18} <- {expr}")
        if self.rejections:
            lines += ["", "rejected (proposal did not survive validation)"]
            for rej in self.rejections:
                lines.append(f"  {rej.category:<18} {rej.reason}")
        if self.skipped:
            lines += ["", "skipped"] + [f"  {s}" for s in self.skipped]
        return "\n".join(lines)


def load_corrections(path: str | Path) -> list[Correction]:
    raw = Path(path).read_text(encoding="utf-8").strip()
    if not raw or raw == "null":
        return []
    data = json.loads(raw)
    if isinstance(data, dict):
        data = data.get("corrections", [])
    out = [Correction.from_json(o) for o in data]
    return [c for c in out if c.merchant_key and c.to_category]


def synthesize(
    corrections: Iterable[Correction],
    provider: Provider | None = None,
    min_support: int = 1,
) -> SynthesisResult:
    """Turn corrections into a validated rule pack.

    min_support is how many corrections a category needs before a rule is
    proposed for it. One is a reasonable default for a single-user app: if
    someone bothered to correct a merchant once, they mean it.
    """
    provider = provider or HeuristicProvider()
    corrections = list(corrections)
    result = SynthesisResult(provider=getattr(provider, "name", "unknown"))

    by_category: dict[str, set[str]] = defaultdict(set)
    for c in corrections:
        by_category[c.to_category].add(c.merchant_key)

    # A merchant the user has filed under two different categories is not a
    # disagreement to resolve by guessing. It usually means the merchant string
    # is genuinely ambiguous, and a rule would be wrong half the time.
    key_categories: dict[str, set[str]] = defaultdict(set)
    for c in corrections:
        key_categories[c.merchant_key].add(c.to_category)
    conflicted = {k for k, cats in key_categories.items() if len(cats) > 1}
    for k in sorted(conflicted):
        result.skipped.append(f"{k!r} was corrected to {len(key_categories[k])} different categories; no rule made")

    for category in sorted(by_category):
        keys = sorted(by_category[category] - conflicted)
        if not keys:
            continue
        if len(keys) < min_support:
            result.skipped.append(f"{category!r} has {len(keys)} corrected merchant(s), below min_support={min_support}")
            continue

        counter_examples = sorted(
            {k for other, ks in by_category.items() if other != category for k in ks} - set(keys) - conflicted
        )

        try:
            proposal = provider.propose(category, keys, counter_examples)
        except Exception as exc:  # a provider failure must not lose the other categories
            result.rejections.append(Rejection(category, f"provider error: {exc}", {}))
            continue

        ok, reason = validate_rule(proposal, must_match=keys, must_not_match=counter_examples)
        if not ok:
            result.rejections.append(Rejection(category, reason, proposal))
            # Falling back to the literal keys is always safe: it matches
            # exactly what the user corrected and nothing else.
            proposal = {
                "merchant_contains": keys,
                "merchant_regex": "",
                "note": "fallback to literal merchants after the proposal failed validation",
            }
            ok, reason = validate_rule(proposal, must_match=keys, must_not_match=counter_examples)
            if not ok:
                continue

        result.rules.append(_build_rule(category, proposal))

    return result


def _build_rule(category: str, proposal: dict[str, Any]) -> dict[str, Any]:
    match: dict[str, Any] = {}
    if proposal.get("merchant_contains"):
        match["merchant_contains"] = proposal["merchant_contains"]
    if proposal.get("merchant_regex"):
        match["merchant_regex"] = proposal["merchant_regex"]
    return {
        "id": f"syn.{_slug(category)}",
        "category": category,
        "priority": SYNTHESIZED_PRIORITY,
        "source": "synthesized",
        "match": match,
        "note": proposal.get("note", ""),
    }


def _slug(s: str) -> str:
    return re.sub(r"[^a-z0-9]+", "_", s.lower()).strip("_")


def validate_rule(
    proposal: dict[str, Any],
    must_match: list[str],
    must_not_match: list[str],
) -> tuple[bool, str]:
    """Check a proposal against the corrections it claims to explain.

    This is the gate that makes it safe to put a language model in the loop:
    whatever it proposes, the rule only ships if it reproduces the user's own
    decisions exactly.
    """
    contains = [s for s in proposal.get("merchant_contains", []) if s]
    regex_src = proposal.get("merchant_regex", "")

    if not contains and not regex_src:
        return False, "proposal has no match expression"

    pattern = None
    if regex_src:
        try:
            pattern = re.compile(regex_src, re.IGNORECASE)
        except re.error as exc:
            return False, f"invalid regex: {exc}"

    # A one- or two-character substring will match half the ledger.
    too_short = [s for s in contains if len(s) < 3]
    if too_short:
        return False, f"substring(s) too short to be safe: {too_short}"

    def matches(key: str) -> bool:
        tight = key.replace(" ", "")
        for s in contains:
            if s in key or s.replace(" ", "") in tight:
                return True
        return bool(pattern and pattern.search(key))

    missed = [k for k in must_match if not matches(k)]
    if missed:
        return False, f"does not cover corrected merchant(s): {missed}"

    caught = [k for k in must_not_match if matches(k)]
    if caught:
        return False, f"would also capture merchant(s) the user filed elsewhere: {caught}"

    return True, ""


def write_pack(result: SynthesisResult, path: str | Path, name: str = "synthesized") -> Path:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(result.pack(name=name), indent=2) + "\n", encoding="utf-8")
    return path
