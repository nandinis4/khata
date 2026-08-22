"""Tests for rule synthesis.

The interesting behaviour under test is not "does the model produce a rule" but
"does a bad rule get thrown away". Every test here uses a stub provider, so the
validation logic is exercised deterministically and the suite needs no API key,
no local model, and no network.
"""

from __future__ import annotations

import json

import pytest

from khata_tools.rulesynth import Correction, HeuristicProvider, load_corrections, synthesize, validate_rule
from khata_tools.rulesynth.providers import _longest_common_substring


class StubProvider:
    """A provider that returns whatever it is told to."""

    def __init__(self, response: dict | Exception, name: str = "stub"):
        self.response = response
        self.name = name
        self.calls: list[tuple[str, list[str], list[str]]] = []

    def propose(self, category, merchant_keys, counter_examples):
        self.calls.append((category, list(merchant_keys), list(counter_examples)))
        if isinstance(self.response, Exception):
            raise self.response
        return self.response


def corr(key: str, to: str, frm: str = "uncategorized") -> Correction:
    return Correction(merchant_key=key, to_category=to, from_category=frm)


# --- validation ------------------------------------------------------------


def test_valid_proposal_is_accepted():
    ok, reason = validate_rule(
        {"merchant_contains": ["tokai"]},
        must_match=["blue tokai", "bluetokai"],
        must_not_match=["swiggy"],
    )
    assert ok, reason


def test_proposal_missing_a_corrected_merchant_is_rejected():
    ok, reason = validate_rule(
        {"merchant_contains": ["tokai"]},
        must_match=["blue tokai", "third wave coffee"],
        must_not_match=[],
    )
    assert not ok
    assert "third wave coffee" in reason


def test_proposal_that_captures_another_category_is_rejected():
    # "mart" covers the corrected merchant, but it also matches a merchant the
    # user deliberately filed under groceries. Shipping it would silently
    # re-file their grocery runs as dining.
    ok, reason = validate_rule(
        {"merchant_contains": ["mart"]},
        must_match=["donut mart"],
        must_not_match=["instamart"],
    )
    assert not ok
    assert "instamart" in reason


def test_dangerously_short_substrings_are_rejected():
    ok, reason = validate_rule({"merchant_contains": ["ub"]}, must_match=["uber"], must_not_match=[])
    assert not ok
    assert "too short" in reason


def test_invalid_regex_is_rejected_not_raised():
    ok, reason = validate_rule({"merchant_regex": "([unclosed"}, must_match=["x"], must_not_match=[])
    assert not ok
    assert "invalid regex" in reason


def test_empty_proposal_is_rejected():
    ok, reason = validate_rule({"merchant_contains": [], "merchant_regex": ""}, must_match=["x"], must_not_match=[])
    assert not ok


def test_validation_matches_unspaced_variants():
    # Mirrors the Go engine, which compares despaced forms so one rule covers
    # both a card terminal's spaced name and a VPA's unspaced handle.
    ok, _ = validate_rule(
        {"merchant_contains": ["blue tokai"]},
        must_match=["bluetokai"],
        must_not_match=[],
    )
    assert ok


# --- synthesis -------------------------------------------------------------


def test_single_correction_produces_a_literal_rule():
    result = synthesize([corr("qzxw traders", "office")])
    assert len(result.rules) == 1
    rule = result.rules[0]
    assert rule["category"] == "office"
    assert rule["source"] == "synthesized"
    assert "qzxw traders" in rule["match"]["merchant_contains"]


def test_shared_fragment_generalises():
    result = synthesize(
        [corr("blue tokai coffee", "cafe"), corr("bluetokai", "cafe"), corr("blue tokai blr", "cafe")],
        provider=HeuristicProvider(),
    )
    assert len(result.rules) == 1
    contains = result.rules[0]["match"]["merchant_contains"]
    assert any("tokai" in c for c in contains)


def test_conflicting_corrections_produce_no_rule():
    # The same merchant filed two ways is ambiguous, and guessing would be wrong
    # roughly half the time.
    result = synthesize([corr("amazon", "shopping"), corr("amazon", "groceries")])
    assert result.rules == []
    assert any("amazon" in s for s in result.skipped)


def test_unsafe_model_proposal_falls_back_to_literals():
    # The stub proposes a substring that would also capture a merchant the user
    # filed under a different category. The rule must not ship as proposed.
    stub = StubProvider({"merchant_contains": ["a"], "merchant_regex": "", "note": "too greedy"})
    result = synthesize([corr("mad over donuts", "dining"), corr("instamart", "groceries")], provider=stub)

    assert len(result.rejections) >= 1
    dining = [r for r in result.rules if r["category"] == "dining"]
    assert len(dining) == 1
    # Fell back to the exact merchant the user corrected.
    assert dining[0]["match"]["merchant_contains"] == ["mad over donuts"]


def test_provider_error_does_not_lose_other_categories():
    stub = StubProvider(RuntimeError("model unavailable"))
    result = synthesize([corr("swiggy", "food"), corr("uber", "transport")], provider=stub)
    assert len(result.rules) == 0
    assert len(result.rejections) == 2
    assert all("model unavailable" in r.reason for r in result.rejections)


def test_min_support_filters_thin_evidence():
    result = synthesize([corr("swiggy", "food")], min_support=2)
    assert result.rules == []
    assert any("min_support" in s for s in result.skipped)


def test_pack_is_wellformed_and_serialisable():
    result = synthesize([corr("swiggy", "food_delivery")])
    pack = result.pack()
    assert pack["name"] == "synthesized"
    assert pack["rules"]
    # Must survive a JSON round trip, since the Go side reads it with a strict
    # decoder that rejects unknown fields.
    round_tripped = json.loads(json.dumps(pack))
    rule = round_tripped["rules"][0]
    assert set(rule) <= {"id", "category", "priority", "source", "match", "note"}
    assert set(rule["match"]) <= {
        "merchant_equals",
        "merchant_contains",
        "merchant_regex",
        "channel",
        "direction",
        "source_package",
        "amount_min_minor",
        "amount_max_minor",
    }


def test_synthesized_rules_outrank_builtins():
    from khata_tools.rulesynth.synth import SYNTHESIZED_PRIORITY

    result = synthesize([corr("uber", "commute")])
    assert result.rules[0]["priority"] == SYNTHESIZED_PRIORITY
    assert SYNTHESIZED_PRIORITY > 10  # above every builtin priority in the shipped pack


def test_load_corrections_reads_cli_export(tmp_path):
    path = tmp_path / "corrections.json"
    path.write_text(
        json.dumps(
            [
                {"merchant_key": "uber", "to_category": "commute", "from_category": "transport"},
                {"merchant_key": "", "to_category": "ignored"},  # unusable, must be dropped
            ]
        )
    )
    got = load_corrections(path)
    assert len(got) == 1
    assert got[0].merchant_key == "uber"


def test_load_corrections_handles_empty_export(tmp_path):
    path = tmp_path / "corrections.json"
    path.write_text("null")
    assert load_corrections(path) == []


@pytest.mark.parametrize(
    "strings,expected",
    [
        (["blue tokai coffee", "bluetokai"], "tokai"),
        (["swiggy", "swiggyinstamart"], "swiggy"),
        (["abc", "xyz"], ""),
    ],
)
def test_longest_common_substring(strings, expected):
    assert _longest_common_substring(strings) == expected
