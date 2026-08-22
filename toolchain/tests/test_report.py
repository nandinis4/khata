"""Tests for the report generator, including the ledger replay it depends on."""

from __future__ import annotations

import json
from datetime import datetime, timedelta, timezone

import pytest

from khata_tools.report import build_report, find_recurring, money, read_ledger
from khata_tools.report.report import Txn

BASE = datetime(2025, 8, 5, 12, 0, tzinfo=timezone.utc)


def txn_event(seq, tid, amount, merchant, category, when, direction="debit"):
    return {
        "seq": seq,
        "at": when.isoformat(),
        "type": "txn",
        "txn": {
            "id": tid,
            "occurred_at": when.isoformat(),
            "amount_minor": amount,
            "direction": direction,
            "category": category,
            "merchant_key": merchant,
            "merchant_raw": merchant.title(),
            "currency": "INR",
        },
    }


def write_log(tmp_path, events):
    path = tmp_path / "ledger.jsonl"
    path.write_text("\n".join(json.dumps(e) for e in events) + "\n")
    return path


def test_money_uses_indian_grouping():
    # Must agree with budget.Money on the Go side; the two are checked against
    # the same table.
    assert money(0) == "0"
    assert money(9900) == "99"
    assert money(100000) == "1,000"
    assert money(12345600) == "1,23,456"
    assert money(18500000) == "1,85,000"
    assert money(45050) == "450.50"
    assert money(-100000) == "-1,000"


def test_read_ledger_replays_category_corrections(tmp_path):
    events = [
        txn_event(1, "a", 45000, "uber", "transport", BASE),
        {
            "seq": 2,
            "at": BASE.isoformat(),
            "type": "category",
            "category": {"transaction_id": "a", "from": "transport", "to": "commute", "source": "user"},
        },
    ]
    txns = read_ledger(write_log(tmp_path, events))
    assert len(txns) == 1
    assert txns[0].category == "commute", "the user's correction must win over the engine's guess"


def test_read_ledger_survives_a_torn_final_line(tmp_path):
    path = tmp_path / "ledger.jsonl"
    good = json.dumps(txn_event(1, "a", 45000, "uber", "transport", BASE))
    path.write_text(good + "\n" + '{"seq":2,"type":"txn","txn":{"id":"b","amo')
    txns = read_ledger(path)
    assert len(txns) == 1


def test_read_ledger_ignores_events_for_unknown_transactions(tmp_path):
    events = [
        {
            "seq": 1,
            "at": BASE.isoformat(),
            "type": "category",
            "category": {"transaction_id": "ghost", "from": "a", "to": "b", "source": "user"},
        }
    ]
    assert read_ledger(write_log(tmp_path, events)) == []


def test_find_recurring_detects_a_monthly_subscription():
    txns = [
        Txn(f"t{i}", BASE + timedelta(days=30 * i), 19900, "debit", "subscriptions", "netflix", "Netflix")
        for i in range(4)
    ]
    found = find_recurring(txns)
    assert len(found) == 1
    assert found[0]["merchant"] == "Netflix"
    assert found[0]["occurrences"] == 4
    assert found[0]["annualised_minor"] == 19900 * 12


def test_find_recurring_ignores_irregular_spending():
    # Same merchant, wildly different amounts and gaps: a coffee habit, not a
    # subscription.
    txns = [
        Txn("t0", BASE, 25000, "debit", "dining", "bluetokai", "Blue Tokai"),
        Txn("t1", BASE + timedelta(days=3), 48000, "debit", "dining", "bluetokai", "Blue Tokai"),
        Txn("t2", BASE + timedelta(days=9), 19000, "debit", "dining", "bluetokai", "Blue Tokai"),
        Txn("t3", BASE + timedelta(days=11), 62000, "debit", "dining", "bluetokai", "Blue Tokai"),
    ]
    assert find_recurring(txns) == []


def test_find_recurring_tolerates_a_small_price_change():
    amounts = [19900, 19900, 21900, 21900]  # a plan price rise mid-year
    txns = [
        Txn(f"t{i}", BASE + timedelta(days=30 * i), amt, "debit", "subscriptions", "netflix", "Netflix")
        for i, amt in enumerate(amounts)
    ]
    assert len(find_recurring(txns)) == 1


def test_report_excludes_transfers_and_income_from_spend(tmp_path):
    events = [
        txn_event(1, "a", 100000, "swiggy", "food_delivery", BASE),
        txn_event(2, "b", 18500000, "salary", "income", BASE, direction="credit"),
        txn_event(3, "c", 5000000, "self", "transfers", BASE),
    ]
    txns = read_ledger(write_log(tmp_path, events))
    spends = [t for t in txns if t.is_spend]
    assert [t.category for t in spends] == ["food_delivery"], "only the debit outside income/transfers is spend"

    text = build_report(txns)
    assert "**Total spend** ₹1,000" in text
    assert "₹1,85,000" in text, "income is still reported, just not as spending"


def test_report_handles_an_empty_ledger(tmp_path):
    path = tmp_path / "ledger.jsonl"
    path.write_text("")
    assert "No spending recorded" in build_report(read_ledger(path))


def test_report_surfaces_small_spend_total(tmp_path):
    events = [txn_event(i, f"t{i}", 15000, f"chai{i}", "dining", BASE + timedelta(hours=i)) for i in range(10)]
    text = build_report(read_ledger(write_log(tmp_path, events)))
    assert "The small ones" in text
    assert "₹1,500" in text


def test_report_compares_against_the_previous_month(tmp_path):
    july = BASE.replace(month=7)
    events = [
        txn_event(1, "a", 100000, "swiggy", "food_delivery", july),
        txn_event(2, "b", 150000, "swiggy", "food_delivery", BASE),
    ]
    text = build_report(read_ledger(write_log(tmp_path, events)), period="2025-08")
    assert "July 2025" in text
    assert "₹500 more" in text


@pytest.mark.parametrize("period", ["2025-08", None])
def test_report_is_valid_markdown_ish(tmp_path, period):
    events = [txn_event(1, "a", 100000, "swiggy", "food_delivery", BASE)]
    text = build_report(read_ledger(write_log(tmp_path, events)), period=period)
    assert text.startswith("# khata report")
    assert text.endswith("\n")
    assert "sent nowhere" in text
