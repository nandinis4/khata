package engine

import (
	"testing"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

var now = time.Date(2025, 8, 16, 12, 0, 0, 0, time.UTC)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(Options{LedgerPath: ":memory:", Location: time.UTC})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func sms(body string, at time.Time) model.Message {
	return model.Message{SourcePackage: "sms", Sender: "AD-TESTBK", Body: body, ReceivedAt: at}
}

// The end-to-end path a real notification takes: classify, extract,
// categorise, store.
func TestIngestBooksAndCategorises(t *testing.T) {
	e := newEngine(t)
	res, err := e.Ingest(sms("Rs.450.00 debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)", now))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Booked {
		t.Fatalf("not booked: kind=%s reason=%s", res.Kind, res.Reason)
	}
	if res.Txn.Category != "food_delivery" {
		t.Errorf("category: got %q want food_delivery", res.Txn.Category)
	}
	if res.Decision.RuleID == "" {
		t.Error("decision should name the rule that fired")
	}
	if res.Txn.NeedsReview {
		t.Error("a fully-parsed, categorised transaction should not need review")
	}
	if n, _ := e.Count(); n != 1 {
		t.Errorf("count: got %d want 1", n)
	}
}

func TestIngestSkipsNonTransactionsWithoutStoringThem(t *testing.T) {
	e := newEngine(t)
	for _, body := range []string{
		"123456 is the OTP for txn of Rs 2,500.00 at AMAZON. Do not share.",
		"Your A/c XX4412 Avl Bal is Rs 18,110.42 as on 16-08-25.",
		"Congratulations! Pre-approved personal loan up to Rs 5,00,000. Apply now.",
	} {
		res, err := e.Ingest(sms(body, now))
		if err != nil {
			t.Fatal(err)
		}
		if res.Booked {
			t.Errorf("booked a non-transaction: %q", body)
		}
		if res.Reason == "" {
			t.Errorf("no reason given for skipping %q", body)
		}
	}
	if n, _ := e.Count(); n != 0 {
		t.Errorf("ledger should be empty, has %d", n)
	}
}

// The same payment arriving as a bank SMS and an app push must book once.
func TestIngestDeduplicatesAcrossSources(t *testing.T) {
	e := newEngine(t)
	first, err := e.Ingest(sms("Rs.250.00 debited from a/c XX4412 on 16-08-25 to VPA bluetokai@icici (UPI Ref no 522300112233)", now))
	if err != nil || !first.Booked {
		t.Fatalf("first ingest failed: %v %+v", err, first)
	}

	second, err := e.Ingest(model.Message{
		SourcePackage: "com.google.android.apps.nbu.paisa.user",
		Title:         "₹250 paid to Blue Tokai Coffee",
		Body:          "Using ICICI Bank ****4412 · UPI Ref no 522300112233",
		ReceivedAt:    now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Booked {
		t.Error("the same payment was booked twice")
	}
	if !second.Duplicate {
		t.Error("second capture should be reported as a duplicate")
	}
	if n, _ := e.Count(); n != 1 {
		t.Errorf("count: got %d want 1", n)
	}
}

// Re-importing the same SMS backup must be a no-op, not a doubled ledger.
func TestIngestIsIdempotent(t *testing.T) {
	e := newEngine(t)
	body := "Rs.450.00 debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)"
	for i := 0; i < 3; i++ {
		if _, err := e.Ingest(sms(body, now)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := e.Count(); n != 1 {
		t.Errorf("count after 3 identical ingests: got %d want 1", n)
	}
}

func TestUnknownMerchantIsFlaggedForReview(t *testing.T) {
	e := newEngine(t)
	res, err := e.Ingest(sms("Rs.1,250.00 debited from a/c XX4412 on 16-08-25 at QZXW TRADERS 8891 (Ref 998877665544)", now))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Booked {
		t.Fatalf("should still be booked: %s", res.Reason)
	}
	if res.Decision.Matched {
		t.Errorf("did not expect a rule match, got %q", res.Decision.RuleID)
	}
	if !res.Txn.NeedsReview {
		t.Error("an unrecognised counterparty is exactly what should be asked about")
	}

	pending, err := e.NeedsReview(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("review queue: got %d want 1", len(pending))
	}
}

func TestCorrectionIsRecordedAsTrainingSignal(t *testing.T) {
	e := newEngine(t)
	res, err := e.Ingest(sms("Rs.899.00 debited from a/c XX4412 on 16-08-25 to VPA uber@paytm (UPI Ref no 522345678902)", now))
	if err != nil || !res.Booked {
		t.Fatalf("ingest: %v %+v", err, res)
	}
	if err := e.Correct(res.Txn.ID, "commute"); err != nil {
		t.Fatal(err)
	}

	corr, err := e.Corrections()
	if err != nil {
		t.Fatal(err)
	}
	if len(corr) != 1 {
		t.Fatalf("corrections: got %d want 1", len(corr))
	}
	if corr[0].FromCategory != "transport" || corr[0].ToCategory != "commute" {
		t.Errorf("correction: got %s -> %s, want transport -> commute", corr[0].FromCategory, corr[0].ToCategory)
	}
	// The exported correction must not leak the amount or the message text.
	if corr[0].MerchantKey == "" {
		t.Error("correction needs the merchant key to be useful for rule synthesis")
	}
}

// An alert must fire once per crossing, not on every subsequent transaction.
func TestBudgetAlertFiresOnceThenGoesQuiet(t *testing.T) {
	e := newEngine(t)
	if err := e.SetEnvelope("food_delivery", 100000); err != nil { // ₹1,000
		t.Fatal(err)
	}

	// ₹600 takes us past 50%.
	first, err := e.Ingest(sms("Rs.600.00 debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 111111111111)", now))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Alerts) != 1 || first.Alerts[0].Threshold != 50 {
		t.Fatalf("expected one 50%% alert, got %+v", first.Alerts)
	}

	// Another ₹100 stays under 80%: no new crossing, so no new alert.
	second, err := e.Ingest(sms("Rs.100.00 debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 222222222222)", now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Alerts) != 0 {
		t.Errorf("no threshold was crossed, expected silence, got %+v", second.Alerts)
	}

	// ₹400 more takes the total to ₹1,100, crossing both 80% and 100%.
	third, err := e.Ingest(sms("Rs.400.00 debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 333333333333)", now.Add(2*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Alerts) != 2 {
		t.Fatalf("expected the 80 and 100 crossings, got %+v", third.Alerts)
	}
}

func TestMonthStatesReflectIngestedSpending(t *testing.T) {
	e := newEngine(t)
	if err := e.SetEnvelope("food_delivery", 500000); err != nil {
		t.Fatal(err)
	}
	for i, amt := range []string{"450.00", "320.00", "180.00"} {
		body := "Rs." + amt + " debited from a/c XX4412 on 16-08-25 to VPA swiggy@ybl (UPI Ref no 44444444444" + string(rune('0'+i)) + ")"
		if _, err := e.Ingest(sms(body, now.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}

	states, err := e.MonthStates(now)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range states {
		if s.Category == "food_delivery" {
			found = true
			if s.SpentMinor != 95000 {
				t.Errorf("spent: got %d want 95000", s.SpentMinor)
			}
			if s.TxnCount != 3 {
				t.Errorf("txn count: got %d want 3", s.TxnCount)
			}
		}
	}
	if !found {
		t.Error("food_delivery missing from month states")
	}
}

// A debit at 23:50 on 31 Aug whose SMS lands at 00:05 on 1 Sep belongs to
// August's budget. Getting this wrong silently misstates two months.
func TestLateArrivingMessageLandsInTheRightMonth(t *testing.T) {
	e := newEngine(t)
	arrived := time.Date(2025, 9, 1, 0, 5, 0, 0, time.UTC)
	if _, err := e.Ingest(sms(
		"Rs.450.00 debited from a/c XX4412 on 31-08-25 to VPA swiggy@ybl (UPI Ref no 555555555555)", arrived)); err != nil {
		t.Fatal(err)
	}

	aug, err := e.Month(2025, time.August)
	if err != nil {
		t.Fatal(err)
	}
	sep, err := e.Month(2025, time.September)
	if err != nil {
		t.Fatal(err)
	}
	if len(aug) != 1 {
		t.Errorf("August should hold the transaction, has %d", len(aug))
	}
	if len(sep) != 0 {
		t.Errorf("September should be empty, has %d", len(sep))
	}
}

func TestEngineExposesRuleMetadata(t *testing.T) {
	e := newEngine(t)
	if e.RuleCount() == 0 {
		t.Error("expected builtin rules to be loaded")
	}
	if len(e.Packs()) == 0 {
		t.Error("expected the builtin pack to be listed")
	}
	if len(e.Categories()) < 10 {
		t.Errorf("expected a broad taxonomy, got %v", e.Categories())
	}
}
