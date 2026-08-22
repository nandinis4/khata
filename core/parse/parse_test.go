package parse

import (
	"testing"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

// All fixtures below are synthetic. They reproduce the message shapes issued
// by major Indian banks and payment apps, with invented amounts, merchants,
// account tails and reference numbers.

var refTime = time.Date(2025, 8, 12, 20, 15, 0, 0, time.UTC)

func msg(body string) model.Message {
	return model.Message{SourcePackage: "sms", Sender: "AD-TESTBK", Body: body, ReceivedAt: refTime}
}

func TestParseDebits(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		amount    int64
		direction model.Direction
		channel   model.Channel
		merchant  string
		account   string
		reference string
	}{
		{
			name:      "hdfc upi sent",
			body:      "Sent Rs.150.00 From HDFC Bank A/C x4412 To SWIGGY On 12/08/25 Ref 522345678901 Not You? Call 18002586161",
			amount:    15000,
			direction: model.DirectionDebit,
			channel:   model.ChannelUnknown,
			merchant:  "swiggy",
			account:   "XX4412",
			reference: "522345678901",
		},
		{
			name:      "hdfc vpa debit",
			body:      "Rs.450.00 debited from a/c XXXXXX4412 on 12-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)",
			amount:    45000,
			direction: model.DirectionDebit,
			channel:   model.ChannelUPI,
			merchant:  "swiggy",
			account:   "XX4412",
			reference: "522345678901",
		},
		{
			name:      "icici debit with counterparty credited",
			body:      "ICICI Bank Acct XX4412 debited for Rs 1,299.00 on 12-Aug-25; AMAZON credited. UPI:522345678901. Call 18002662 for dispute.",
			amount:    129900,
			direction: model.DirectionDebit,
			channel:   model.ChannelUPI,
			merchant:  "amazon",
			account:   "XX4412",
			reference: "522345678901",
		},
		{
			name:      "sbi bare amount",
			body:      "Dear UPI user A/C X4412 debited by 150.0 on date 12Aug25 trf to SWIGGY Refno 522345678901. If not u? call 1800111109. -SBI",
			amount:    15000,
			direction: model.DirectionDebit,
			channel:   model.ChannelUPI,
			merchant:  "swiggy",
			account:   "XX4412",
			reference: "522345678901",
		},
		{
			name:      "axis card with available limit",
			body:      "Spent Card no. XX5678 INR 2450.00 12-08-25 12:30:45 BIGBASKET Avl Lmt INR 47550.00",
			amount:    245000,
			direction: model.DirectionDebit,
			channel:   model.ChannelCard,
			merchant:  "bigbasket",
			account:   "XX5678",
		},
		{
			name:      "debit card with balance trailer",
			body:      "Alert: You've spent Rs.899.00 via Debit Card xx4412 at UBER INDIA on 2025-08-12:19:22:11. Avl bal: Rs.23,110.42",
			amount:    89900,
			direction: model.DirectionDebit,
			channel:   model.ChannelCard,
			merchant:  "uber",
			account:   "XX4412",
		},
		{
			name:      "atm withdrawal",
			body:      "Rs 5000.00 withdrawn from A/c XX4412 at ATM on 12-08-25. Avl Bal Rs 18110.42",
			amount:    500000,
			direction: model.DirectionDebit,
			channel:   model.ChannelATM,
			account:   "XX4412",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(msg(tc.body))
			if !got.Bookable {
				t.Fatalf("expected bookable, got kind=%s reason=%q", got.Kind, got.Reason)
			}
			tx := got.Txn
			if tx.AmountMinor != tc.amount {
				t.Errorf("amount: got %d want %d", tx.AmountMinor, tc.amount)
			}
			if tx.Direction != tc.direction {
				t.Errorf("direction: got %s want %s", tx.Direction, tc.direction)
			}
			if tc.channel != model.ChannelUnknown && tx.Channel != tc.channel {
				t.Errorf("channel: got %s want %s", tx.Channel, tc.channel)
			}
			if tc.merchant != "" && tx.MerchantKey != tc.merchant {
				t.Errorf("merchant key: got %q want %q (raw %q)", tx.MerchantKey, tc.merchant, tx.MerchantRaw)
			}
			if tc.account != "" && tx.AccountHint != tc.account {
				t.Errorf("account: got %q want %q", tx.AccountHint, tc.account)
			}
			if tc.reference != "" && tx.Reference != tc.reference {
				t.Errorf("reference: got %q want %q", tx.Reference, tc.reference)
			}
		})
	}
}

func TestParseCredits(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		amount   int64
		merchant string
	}{
		{
			name:     "salary credit",
			body:     "Dear Customer, Acct XX4412 is credited with Rs 1,85,000.00 on 01-Aug-25 from SALARY. Avl Bal Rs 2,03,110.42",
			amount:   18500000,
			merchant: "salary",
		},
		{
			name:     "imps inbound",
			body:     "Your a/c no. XXXXXXXX4412 is credited by Rs.8,500.00 on 01-08-25 by a/c linked to mobile 9XXXXXX210 (IMPS Ref no 521312345678)",
			amount:   850000,
			merchant: "",
		},
		{
			name:     "refund reversal",
			body:     "Rs.1,299.00 has been reversed and credited to your Card xx5678 by AMAZON on 14-08-25.",
			amount:   129900,
			merchant: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(msg(tc.body))
			if !got.Bookable {
				t.Fatalf("expected bookable, got kind=%s reason=%q", got.Kind, got.Reason)
			}
			if got.Txn.Direction != model.DirectionCredit {
				t.Errorf("direction: got %s want credit", got.Txn.Direction)
			}
			if got.Txn.AmountMinor != tc.amount {
				t.Errorf("amount: got %d want %d", got.Txn.AmountMinor, tc.amount)
			}
			if tc.merchant != "" && got.Txn.MerchantKey != tc.merchant {
				t.Errorf("merchant: got %q want %q", got.Txn.MerchantKey, tc.merchant)
			}
		})
	}
}

// The rejection suite is the one that matters most in practice: these messages
// all contain amounts and would corrupt a budget if booked.
func TestRejectsNonTransactions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Kind
	}{
		{
			name: "otp with amount",
			body: "123456 is the OTP for txn of Rs 2,500.00 at AMAZON on card xx5678. Valid for 10 min. Do not share with anyone.",
			want: KindOTP,
		},
		{
			name: "otp alternate phrasing",
			body: "Use OTP 884213 to authorise payment of Rs 4,999 to FLIPKART. Never share this code.",
			want: KindOTP,
		},
		{
			name: "balance enquiry",
			body: "Your A/c XX4412 Avl Bal is Rs 18,110.42 as on 12-08-25. -TESTBK",
			want: KindBalance,
		},
		{
			name: "declined transaction",
			body: "Your txn of Rs 500.00 at SWIGGY on card xx5678 has been declined due to insufficient balance.",
			want: KindDeclined,
		},
		{
			name: "autopay advance notice",
			body: "Your UPI Autopay mandate of Rs 199.00 for NETFLIX will be debited on 15-08-25 from A/c XX4412.",
			want: KindMandateNotice,
		},
		{
			name: "collect request",
			body: "RAHUL has requested Rs 800.00 via UPI. Approve in your app before 12-08-25 21:00.",
			want: KindCollectReq,
		},
		{
			name: "loan marketing",
			body: "Congratulations! You are pre-approved for a personal loan up to Rs 5,00,000 at 10.5% p.a. Apply now. T&C apply.",
			want: KindPromotional,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(msg(tc.body))
			if got.Bookable {
				t.Fatalf("message was booked but should not be: %+v", got.Txn)
			}
			if got.Kind != tc.want {
				t.Errorf("kind: got %s want %s", got.Kind, tc.want)
			}
			if got.Reason == "" {
				t.Error("expected a human-readable rejection reason")
			}
		})
	}
}

// A debit alert that quotes both the spend and the remaining balance must book
// the spend. This is the single most common way naive parsers go wrong.
func TestPicksSpendNotBalance(t *testing.T) {
	cases := []struct {
		body string
		want int64
	}{
		{"Alert: You've spent Rs.899.00 via Debit Card xx4412 at UBER INDIA. Avl bal: Rs.23,110.42", 89900},
		{"Rs 5000.00 withdrawn from A/c XX4412 at ATM on 12-08-25. Avl Bal Rs 18110.42", 500000},
		{"Spent Card no. XX5678 INR 2450.00 12-08-25 12:30:45 BIGBASKET Avl Lmt INR 47550.00", 245000},
		{"Dear Customer, Acct XX4412 is credited with Rs 1,85,000.00 on 01-Aug-25 from SALARY. Avl Bal Rs 2,03,110.42", 18500000},
	}
	for _, tc := range cases {
		got := Parse(msg(tc.body))
		if !got.Bookable {
			t.Fatalf("not bookable: %q", tc.body)
		}
		if got.Txn.AmountMinor != tc.want {
			t.Errorf("got %d want %d for %q", got.Txn.AmountMinor, tc.want, tc.body)
		}
	}
}

func TestNotificationTitleAndBodyAreCombined(t *testing.T) {
	m := model.Message{
		SourcePackage: "com.google.android.apps.nbu.paisa.user",
		Title:         "₹250 paid to Blue Tokai Coffee",
		Body:          "Using ICICI Bank ****4412 · UPI Ref no 522300112233",
		ReceivedAt:    refTime,
	}
	got := Parse(m)
	if !got.Bookable {
		t.Fatalf("expected bookable, got %s / %s", got.Kind, got.Reason)
	}
	if got.Txn.AmountMinor != 25000 {
		t.Errorf("amount: got %d want 25000", got.Txn.AmountMinor)
	}
	if got.Txn.Direction != model.DirectionDebit {
		t.Errorf("direction: got %s want debit", got.Txn.Direction)
	}
	if got.Txn.MerchantKey != "blue tokai coffee" {
		t.Errorf("merchant: got %q want %q", got.Txn.MerchantKey, "blue tokai coffee")
	}
	if got.Txn.SourcePackage != m.SourcePackage {
		t.Error("source package not carried through")
	}
}

// The transaction date must come from the message, not from arrival time.
// A debit made at 23:50 whose SMS lands at 00:05 belongs to the previous day,
// and in month-boundary cases to the previous budget period.
func TestUsesMessageDateNotArrivalTime(t *testing.T) {
	arrived := time.Date(2025, 9, 1, 0, 5, 0, 0, time.UTC)
	m := model.Message{
		SourcePackage: "sms",
		Body:          "Rs.450.00 debited from a/c XX4412 on 31-08-25 to VPA swiggy@ybl (UPI Ref no 522345678999)",
		ReceivedAt:    arrived,
	}
	got := Parse(m)
	if !got.Bookable {
		t.Fatal("expected bookable")
	}
	if got.Txn.OccurredAt.Month() != time.August || got.Txn.OccurredAt.Day() != 31 {
		t.Errorf("occurred_at: got %s want 31 Aug", got.Txn.OccurredAt.Format("2006-01-02"))
	}
}

func TestConfidenceReflectsExtractionQuality(t *testing.T) {
	rich := Parse(msg("Rs.450.00 debited from a/c XXXXXX4412 on 12-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)"))
	sparse := Parse(msg("Rs 200 debited"))

	if !rich.Bookable || !sparse.Bookable {
		t.Fatal("both fixtures should be bookable")
	}
	if rich.Txn.Confidence <= sparse.Txn.Confidence {
		t.Errorf("rich parse (%.2f) should outscore sparse parse (%.2f)",
			rich.Txn.Confidence, sparse.Txn.Confidence)
	}
	if rich.Txn.NeedsReview {
		t.Error("a fully-extracted transaction should not need review")
	}
	if !sparse.Txn.NeedsReview {
		t.Error("a bare amount with no counterparty should be flagged for review")
	}
}

// The same payment arriving as both a bank SMS and an app push must collapse
// to one dedup key, because they share a reference number.
func TestDedupKeyMatchesAcrossSources(t *testing.T) {
	sms := Parse(model.Message{
		SourcePackage: "sms",
		Body:          "Rs.250.00 debited from a/c XX4412 on 12-08-25 to VPA bluetokai@icici (UPI Ref no 522300112233)",
		ReceivedAt:    refTime,
	})
	push := Parse(model.Message{
		SourcePackage: "com.google.android.apps.nbu.paisa.user",
		Title:         "₹250 paid to Blue Tokai Coffee",
		Body:          "Using ICICI Bank ****4412 · UPI Ref no 522300112233",
		ReceivedAt:    refTime.Add(3 * time.Second),
	})

	if !sms.Bookable || !push.Bookable {
		t.Fatal("both should parse")
	}
	if sms.Txn.DedupKey() != push.Txn.DedupKey() {
		t.Errorf("dedup keys differ:\n sms  %s\n push %s", sms.Txn.DedupKey(), push.Txn.DedupKey())
	}
	if sms.Txn.ID == push.Txn.ID {
		t.Error("ids should differ; dedup is the ledger's job, not the id's")
	}
}

func TestNormalizeMerchant(t *testing.T) {
	cases := map[string]string{
		"RAZ*BLUE TOKAI COFFEE BLR":          "blue tokai coffee",
		"bluetokai@icici":                    "bluetokai",
		"Blue Tokai Coffee Roasters Pvt Ltd": "blue tokai coffee roasters",
		"SWIGGYINSTAMART*BLR":                "swiggyinstamart",
		"PAYTM*UBER INDIA":                   "uber",
		"UPI/SWIGGY/522345678901":            "swiggy",
		"AMAZON PAY INDIA PVT LTD":           "amazon",
		"":                                   "",
	}
	for in, want := range cases {
		if got := NormalizeMerchant(in); got != want {
			t.Errorf("NormalizeMerchant(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMoneyIsExact(t *testing.T) {
	cases := map[string]int64{
		"1,299.50":    129950,
		"150":         15000,
		"150.0":       15000,
		"0.99":        99,
		"1,85,000.00": 18500000,
		"99.999":      9999, // extra precision truncated, never rounded up
	}
	for in, want := range cases {
		got, err := parseMoney(in)
		if err != nil {
			t.Fatalf("parseMoney(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("parseMoney(%q) = %d, want %d", in, got, want)
		}
	}
}

func BenchmarkParse(b *testing.B) {
	m := msg("Rs.450.00 debited from a/c XXXXXX4412 on 12-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Parse(m)
	}
}

// Regression tests for merchant extraction bugs found by running the sample
// corpus end to end. Each of these produced a visibly wrong ledger row.
func TestMerchantExtractionRegressions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // expected MerchantKey
	}{
		{
			// The comma inside "Rs.1,299.00" looked like a clause boundary, so
			// the ICICI "; X credited" pattern captured "299.00 has been
			// reversed and" as the counterparty.
			name: "amount comma is not a clause boundary",
			body: "Rs.1,299.00 has been reversed and credited to your Card xx5678 by AMAZON on 14-08-25.",
			want: "amazon",
		},
		{
			// "(UPI Ref no ...)" ran straight into the merchant name, and the
			// bare "VPA " prefix was kept when the handle had no @.
			name: "vpa prefix and ref bracket stripped",
			body: "Rs.640.00 debited from a/c XX4412 on 18-08-25 to VPA QZXW TRADERS 8891 (UPI Ref no 522345678914)",
			want: "qzxw traders",
		},
		{
			name: "trailing bracket does not leak into card merchant",
			body: "Spent Card no. XX5678 INR 500.00 18-08-25 12:00:00 at CROMA (STORE 42) Avl Lmt INR 4000.00",
			want: "croma",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(msg(tc.body))
			if !got.Bookable {
				t.Fatalf("not bookable: %s", got.Reason)
			}
			if got.Txn.MerchantKey != tc.want {
				t.Errorf("merchant key: got %q want %q (raw %q)",
					got.Txn.MerchantKey, tc.want, got.Txn.MerchantRaw)
			}
		})
	}
}
