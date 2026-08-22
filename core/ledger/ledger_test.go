package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

var base = time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)

func tx(id string, amount int64, merchant string, at time.Time, opts ...func(*model.Transaction)) model.Transaction {
	t := model.Transaction{
		ID:          id,
		OccurredAt:  at,
		CapturedAt:  at,
		AmountMinor: amount,
		Currency:    "INR",
		Direction:   model.DirectionDebit,
		Channel:     model.ChannelUPI,
		MerchantRaw: merchant,
		MerchantKey: merchant,
		Category:    "food_delivery",
	}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func withRef(r string) func(*model.Transaction) {
	return func(t *model.Transaction) { t.Reference = r }
}
func withCategory(c string) func(*model.Transaction) {
	return func(t *model.Transaction) { t.Category = c }
}
func withCredit() func(*model.Transaction) {
	return func(t *model.Transaction) { t.Direction = model.DirectionCredit }
}

func mustOpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertAndCount(t *testing.T) {
	s := mustOpen(t, ":memory:")
	if err := s.Insert(tx("a", 15000, "swiggy", base)); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(tx("b", 25000, "zomato", base.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(); n != 2 {
		t.Errorf("count: got %d want 2", n)
	}
}

func TestRejectsExactDuplicate(t *testing.T) {
	s := mustOpen(t, ":memory:")
	a := tx("a", 15000, "swiggy", base, withRef("522300112233"))
	if err := s.Insert(a); err != nil {
		t.Fatal(err)
	}
	// Same payment seen by a different app: different id, same reference.
	b := tx("b", 15000, "swiggy", base.Add(2*time.Second), withRef("522300112233"))
	if err := s.Insert(b); err != ErrDuplicate {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
	if n, _ := s.Count(); n != 1 {
		t.Errorf("count: got %d want 1", n)
	}
}

// Without a reference number the only defence is the time-window probe.
func TestRejectsFuzzyDuplicateWithinWindow(t *testing.T) {
	s := mustOpen(t, ":memory:")
	if err := s.Insert(tx("a", 15000, "swiggy", base)); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(tx("b", 15000, "swiggy", base.Add(90*time.Second))); err != ErrDuplicate {
		t.Fatalf("within window: got %v, want ErrDuplicate", err)
	}
	// Outside the window it is a genuine second payment and must be kept.
	if err := s.Insert(tx("c", 15000, "swiggy", base.Add(10*time.Minute))); err != nil {
		t.Fatalf("outside window: %v", err)
	}
	if n, _ := s.Count(); n != 2 {
		t.Errorf("count: got %d want 2", n)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "khata.log")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Insert(tx("a", 15000, "swiggy", base, withRef("R1"))); err != nil {
		t.Fatal(err)
	}
	if err := s1.SetEnvelope(model.Envelope{Category: "food_delivery", LimitMinor: 500000}); err != nil {
		t.Fatal(err)
	}
	if err := s1.SetCategory("a", "dining", "user"); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2 := mustOpen(t, path)
	if n, _ := s2.Count(); n != 1 {
		t.Fatalf("count after reopen: got %d want 1", n)
	}
	txns, _ := s2.Between(base.Add(-time.Hour), base.Add(time.Hour))
	if len(txns) != 1 || txns[0].Category != "dining" {
		t.Errorf("category not replayed: %+v", txns)
	}
	envs, _ := s2.Envelopes()
	if len(envs) != 1 || envs[0].LimitMinor != 500000 {
		t.Errorf("envelope not replayed: %+v", envs)
	}
	corr, _ := s2.Corrections()
	if len(corr) != 1 {
		t.Fatalf("corrections after reopen: got %d want 1", len(corr))
	}
	if corr[0].FromCategory != "food_delivery" || corr[0].ToCategory != "dining" {
		t.Errorf("correction wrong: %+v", corr[0])
	}
}

// A power loss mid-append leaves a line with no terminating newline. The log
// is valid up to the last complete record and must open cleanly.
func TestRepairsTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "khata.log")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Insert(tx("a", 15000, "swiggy", base, withRef("R1"))); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":2,"type":"txn","txn":{"id":"b","amo`) // truncated write
	f.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("expected torn tail to be repaired, got: %v", err)
	}
	defer s2.Close()
	if n, _ := s2.Count(); n != 1 {
		t.Errorf("count: got %d want 1", n)
	}
	// The repaired log must be writable again.
	if err := s2.Insert(tx("c", 22000, "zepto", base.Add(time.Hour), withRef("R2"))); err != nil {
		t.Fatalf("insert after repair: %v", err)
	}
	s2.Close()

	s3 := mustOpen(t, path)
	if n, _ := s3.Count(); n != 2 {
		t.Errorf("count after repair+append+reopen: got %d want 2", n)
	}
}

// A complete but unparseable line is corruption, not a torn write, and must
// not be silently swallowed.
func TestRejectsCorruptCompleteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "khata.log")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected corruption to be reported")
	}
}

func TestSpentByCategoryNetsRefundsAndSkipsTransfers(t *testing.T) {
	s := mustOpen(t, ":memory:")
	from, to := base.Add(-24*time.Hour), base.Add(24*time.Hour)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Insert(tx("a", 200000, "amazon", base, withCategory("shopping"), withRef("R1"))))
	must(s.Insert(tx("b", 50000, "amazon", base.Add(time.Hour), withCategory("shopping"), withCredit(), withRef("R2"))))
	must(s.Insert(tx("c", 18500000, "salary", base, withCategory("income"), withCredit(), withRef("R3"))))
	must(s.Insert(tx("d", 100000, "self", base, withCategory("transfers"), withRef("R4"))))

	totals, counts, err := s.SpentByCategory(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := totals["shopping"]; got != 150000 {
		t.Errorf("shopping net: got %d want 150000 (200000 spent less 50000 refunded)", got)
	}
	if counts["shopping"] != 2 {
		t.Errorf("shopping count: got %d want 2", counts["shopping"])
	}
	if _, ok := totals["income"]; ok {
		t.Error("income must not appear as spending")
	}
	if _, ok := totals["transfers"]; ok {
		t.Error("transfers must not appear as spending")
	}
}

func TestRefundExceedingSpendClampsToZero(t *testing.T) {
	s := mustOpen(t, ":memory:")
	if err := s.Insert(tx("a", 50000, "amazon", base, withCategory("shopping"), withCredit(), withRef("R1"))); err != nil {
		t.Fatal(err)
	}
	totals, _, _ := s.SpentByCategory(base.Add(-time.Hour), base.Add(time.Hour))
	if totals["shopping"] != 0 {
		t.Errorf("got %d, want 0: a net-negative category is zero spend, not negative", totals["shopping"])
	}
}

func TestAlertFiresOncePerCrossing(t *testing.T) {
	s := mustOpen(t, ":memory:")
	first, err := s.MarkAlertFired("2025-08", "food_delivery", 80)
	if err != nil || !first {
		t.Fatalf("first call should win: %v %v", first, err)
	}
	again, err := s.MarkAlertFired("2025-08", "food_delivery", 80)
	if err != nil || again {
		t.Fatalf("second call should lose: %v %v", again, err)
	}
	// A different threshold, category or month is a distinct crossing.
	for _, c := range []struct {
		p, cat string
		th     int
	}{
		{"2025-08", "food_delivery", 100},
		{"2025-08", "transport", 80},
		{"2025-09", "food_delivery", 80},
	} {
		ok, err := s.MarkAlertFired(c.p, c.cat, c.th)
		if err != nil || !ok {
			t.Errorf("expected %v to fire: %v %v", c, ok, err)
		}
	}
}

func TestNonUserCategoryChangeIsNotACorrection(t *testing.T) {
	s := mustOpen(t, ":memory:")
	if err := s.Insert(tx("a", 15000, "swiggy", base, withCategory("uncategorized"), withRef("R1"))); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategory("a", "food_delivery", "builtin"); err != nil {
		t.Fatal(err)
	}
	corr, _ := s.Corrections()
	if len(corr) != 0 {
		t.Errorf("engine-applied categories are not training signal, got %d corrections", len(corr))
	}
}

func TestCompactionPreservesStateAndCorrections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "khata.log")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range []string{"swiggy", "zomato", "zepto"} {
		if err := s1.Insert(tx(string(rune('a'+i)), int64(10000*(i+1)), m,
			base.Add(time.Duration(i)*time.Hour), withRef("R"+m))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s1.SetEnvelope(model.Envelope{Category: "food_delivery", LimitMinor: 500000}); err != nil {
		t.Fatal(err)
	}
	// Several changes to the same row: compaction should collapse these.
	for _, c := range []string{"dining", "groceries", "dining"} {
		if err := s1.SetCategory("a", c, "user"); err != nil {
			t.Fatal(err)
		}
	}

	beforeSize := fileSize(t, path)
	if err := s1.Compact(); err != nil {
		t.Fatal(err)
	}
	afterSize := fileSize(t, path)
	if afterSize >= beforeSize {
		t.Errorf("compaction did not shrink the log: %d -> %d", beforeSize, afterSize)
	}
	s1.Close()

	s2 := mustOpen(t, path)
	if n, _ := s2.Count(); n != 3 {
		t.Errorf("count after compaction: got %d want 3", n)
	}
	txns, _ := s2.Between(base.Add(-time.Hour), base.Add(24*time.Hour))
	var found bool
	for _, tr := range txns {
		if tr.ID == "a" {
			found = true
			if tr.Category != "dining" {
				t.Errorf("final category: got %q want dining", tr.Category)
			}
		}
	}
	if !found {
		t.Error("transaction a missing after compaction")
	}
	envs, _ := s2.Envelopes()
	if len(envs) != 1 {
		t.Errorf("envelopes after compaction: got %d want 1", len(envs))
	}
	if corr, _ := s2.Corrections(); len(corr) != 3 {
		t.Errorf("corrections after compaction: got %d want 3", len(corr))
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestLedgerFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "khata.log")
	s := mustOpen(t, path)
	if err := s.Insert(tx("a", 15000, "swiggy", base, withRef("R1"))); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("ledger permissions: got %o want 600", perm)
	}
}

func TestNeedsReviewOrdersNewestFirst(t *testing.T) {
	s := mustOpen(t, ":memory:")
	for i := 0; i < 3; i++ {
		tr := tx(string(rune('a'+i)), 1000, "unknown", base.Add(time.Duration(i)*time.Hour),
			withCategory("uncategorized"), withRef("R"+string(rune('a'+i))))
		if err := s.Insert(tr); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.NeedsReview(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d want 3", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].OccurredAt.Before(out[i].OccurredAt) {
			t.Error("not ordered newest first")
		}
	}
}
