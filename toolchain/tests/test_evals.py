"""Tests for the evaluation harness itself.

A harness that cannot fail is worse than no harness: it reports green forever
while the thing it measures rots. These tests drive it with stub results to
prove it detects each class of error, and one integration test runs the real Go
binary end to end.
"""

from __future__ import annotations

import shutil

import pytest

from khata_tools.engine import Engine, ParseResult
from khata_tools.evals import Case, evaluate, load_corpus
from khata_tools.cli import DEFAULT_CORPUS


class StubEngine:
    """Returns canned results in the order the cases were given."""

    def __init__(self, results: list[ParseResult]):
        self.results = results

    def parse_batch(self, messages):
        list(messages)  # consume the generator, as the real engine does
        return self.results


def booked(**kw) -> ParseResult:
    base = dict(
        input="x",
        bookable=True,
        kind="transaction",
        amount_minor=15000,
        direction="debit",
        merchant_key="swiggy",
        category="food_delivery",
    )
    base.update(kw)
    return ParseResult(**base)


def rejected(kind="otp") -> ParseResult:
    return ParseResult(input="x", bookable=False, kind=kind, reason="stubbed")


def test_perfect_run_reports_no_failures():
    cases = [Case("c1", "text", {"bookable": True, "amount_minor": 15000, "category": "food_delivery"})]
    report = evaluate(cases, StubEngine([booked()]))
    assert report.failures == []
    assert report.case_accuracy == 1.0


def test_false_booking_is_reported_separately():
    # The case says this is an OTP; the engine booked it as spending.
    cases = [Case("otp1", "123456 is your OTP for Rs 500", {"bookable": False, "kind": "otp"})]
    report = evaluate(cases, StubEngine([booked(kind="transaction")]))

    assert len(report.false_bookings) == 1
    assert report.missed_bookings == []
    assert "must be 0" in report.render()


def test_missed_booking_is_reported_separately():
    cases = [Case("txn1", "Rs.150 debited", {"bookable": True, "amount_minor": 15000})]
    report = evaluate(cases, StubEngine([rejected(kind="unrelated")]))

    assert len(report.missed_bookings) == 1
    assert report.false_bookings == []


def test_field_errors_are_not_scored_when_the_booking_decision_was_wrong():
    # A single wrong booking decision must not cascade into a dozen misleading
    # field failures; only the decision itself is counted.
    cases = [
        Case(
            "txn1",
            "text",
            {"bookable": True, "amount_minor": 15000, "merchant_key": "swiggy", "category": "food_delivery"},
        )
    ]
    report = evaluate(cases, StubEngine([rejected()]))
    assert [f.field for f in report.failures] == ["bookable"]


def test_wrong_amount_is_caught():
    cases = [Case("c1", "text", {"bookable": True, "amount_minor": 15000})]
    report = evaluate(cases, StubEngine([booked(amount_minor=2311042)]))
    assert len(report.failures) == 1
    assert report.failures[0].field == "amount_minor"
    assert report.fields["amount_minor"].accuracy == 0.0


def test_unknown_expect_field_is_flagged_rather_than_ignored():
    cases = [Case("c1", "text", {"bookable": True, "amont_minor": 15000})]  # typo
    report = evaluate(cases, StubEngine([booked()]))
    assert any("amont_minor" in e for e in report.config_errors)


def test_result_count_mismatch_is_flagged():
    cases = [Case("a", "one", {}), Case("b", "two", {})]
    report = evaluate(cases, StubEngine([booked()]))
    assert report.config_errors


def test_corpus_loads_and_is_wellformed():
    cases = load_corpus(DEFAULT_CORPUS)
    assert len(cases) >= 40
    ids = [c.id for c in cases]
    assert len(ids) == len(set(ids)), "duplicate case ids"
    for c in cases:
        assert c.text.strip(), f"{c.id} has no message text"
        assert not c.unknown_fields(), f"{c.id} asserts on unknown fields {c.unknown_fields()}"
        assert "\n" not in c.text, f"{c.id} spans multiple lines and would desync the batch protocol"


def test_corpus_covers_both_outcomes():
    cases = load_corpus(DEFAULT_CORPUS)
    bookable = [c for c in cases if c.expect.get("bookable") is True]
    rejects = [c for c in cases if c.expect.get("bookable") is False]
    assert len(bookable) >= 20
    assert len(rejects) >= 8, "the rejection cases are the ones that protect the budget"


@pytest.mark.skipif(not shutil.which("go"), reason="needs the Go toolchain to build the engine")
def test_end_to_end_against_the_real_engine():
    """The one test that actually runs the parser under measurement."""
    cases = load_corpus(DEFAULT_CORPUS)
    report = evaluate(cases, Engine())

    assert not report.config_errors, report.config_errors
    assert report.false_bookings == [], (
        "a non-transaction was booked as spending:\n" + "\n".join(str(f) for f in report.false_bookings)
    )
    assert report.case_accuracy >= 0.95, "\n" + report.render()
