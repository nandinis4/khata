package budget

import (
	"testing"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

func envs(pairs ...any) []model.Envelope {
	var out []model.Envelope
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, model.Envelope{Category: pairs[i].(string), LimitMinor: int64(pairs[i+1].(int))})
	}
	return out
}

func find(states []model.EnvelopeState, category string) model.EnvelopeState {
	for _, s := range states {
		if s.Category == category {
			return s
		}
	}
	return model.EnvelopeState{Category: "<missing>"}
}

// Mid-month, 15 of 31 days elapsed.
var midAugust = time.Date(2025, 8, 16, 12, 0, 0, 0, time.UTC)

func TestStatusThresholds(t *testing.T) {
	cases := []struct {
		name       string
		limit      int
		spent      int64
		wantStatus string
	}{
		{"comfortably under", 1000000, 100000, StatusOK},
		{"pace implies overrun", 1000000, 600000, StatusProjectedOver},
		{"past the warn line", 1000000, 850000, StatusWarn},
		{"exactly at the limit", 1000000, 1000000, StatusOver},
		{"past the limit", 1000000, 1200000, StatusOver},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := find(Evaluate(envs("food_delivery", tc.limit),
				map[string]int64{"food_delivery": tc.spent},
				map[string]int{"food_delivery": 3}, midAugust), "food_delivery")
			if st.Status != tc.wantStatus {
				t.Errorf("status: got %s want %s (pct %.1f, projected %d)",
					st.Status, tc.wantStatus, st.Pct, st.ProjectedMinor)
			}
		})
	}
}

// Half the month gone and half the budget spent is on track, not a warning.
func TestOnPaceIsNotFlagged(t *testing.T) {
	st := find(Evaluate(envs("transport", 1000000),
		map[string]int64{"transport": 480000},
		map[string]int{"transport": 12}, midAugust), "transport")
	if st.Status != StatusOK {
		t.Errorf("spending at pace should be ok, got %s (projected %d of 1000000)", st.Status, st.ProjectedMinor)
	}
}

// A straight-line projection on day one would turn one coffee into a
// five-figure forecast. The elapsed-days floor prevents that.
func TestProjectionIsSaneOnTheFirstDay(t *testing.T) {
	firstMorning := time.Date(2025, 8, 1, 7, 0, 0, 0, time.UTC)
	st := find(Evaluate(envs("dining", 500000),
		map[string]int64{"dining": 25000},
		map[string]int{"dining": 1}, firstMorning), "dining")

	// With the 0.5-day floor the projection is 31x a half-day of spending, not
	// 200x a few hours of it.
	if st.ProjectedMinor > 2_000_000 {
		t.Errorf("projection %d is implausible from a single ₹250 coffee", st.ProjectedMinor)
	}
	if st.Status == StatusOver {
		t.Error("one small purchase on day one must not read as over budget")
	}
}

func TestSpendingWithoutAnEnvelopeIsStillReported(t *testing.T) {
	states := Evaluate(nil, map[string]int64{"shopping": 250000}, map[string]int{"shopping": 2}, midAugust)
	st := find(states, "shopping")
	if st.Category != "shopping" {
		t.Fatal("unbudgeted spending should still appear")
	}
	if st.LimitMinor != 0 || st.Status != StatusOK {
		t.Errorf("unbudgeted category should have no limit and no alarm, got %+v", st)
	}
}

func TestEnvelopeWithNoSpendingStillListed(t *testing.T) {
	states := Evaluate(envs("health", 300000), map[string]int64{}, map[string]int{}, midAugust)
	if len(states) != 1 || states[0].Category != "health" {
		t.Fatalf("a budget with no spending must not vanish: %+v", states)
	}
	if states[0].SpentMinor != 0 || states[0].Status != StatusOK {
		t.Errorf("got %+v", states[0])
	}
}

func TestBudgetedCategoriesSortFirst(t *testing.T) {
	states := Evaluate(
		envs("food_delivery", 100000),
		map[string]int64{"food_delivery": 90000, "shopping": 9999999},
		map[string]int{"food_delivery": 4, "shopping": 1},
		midAugust)
	if states[0].Category != "food_delivery" {
		t.Errorf("budgeted category should lead the list, got %q", states[0].Category)
	}
}

func TestAlertsFireForEveryCrossedThreshold(t *testing.T) {
	states := Evaluate(envs("food_delivery", 1000000),
		map[string]int64{"food_delivery": 850000},
		map[string]int{"food_delivery": 9}, midAugust)

	alerts := AlertsFor(states)
	if len(alerts) != 2 {
		t.Fatalf("85%% should cross the 50 and 80 thresholds, got %d: %+v", len(alerts), alerts)
	}
	for _, a := range alerts {
		if a.Title == "" || a.Body == "" {
			t.Errorf("alert missing copy: %+v", a)
		}
	}
}

func TestNoAlertsWithoutABudget(t *testing.T) {
	states := Evaluate(nil, map[string]int64{"shopping": 9999999}, map[string]int{"shopping": 3}, midAugust)
	if got := AlertsFor(states); len(got) != 0 {
		t.Errorf("a category with no limit cannot be over it, got %+v", got)
	}
}

// Amounts must read naturally to an Indian user: 2,50,000 not 250,000.
func TestMoneyUsesIndianDigitGrouping(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		9900:       "99",
		100000:     "1,000",
		12345600:   "1,23,456",
		18500000:   "1,85,000",
		100000000:  "10,00,000",
		1234567890: "1,23,45,678.90",
		45050:      "450.50",
	}
	for minor, want := range cases {
		if got := Money(minor); got != want {
			t.Errorf("Money(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestAlertBodyReportsOverspendAsOverspend(t *testing.T) {
	states := Evaluate(envs("dining", 500000),
		map[string]int64{"dining": 620000},
		map[string]int{"dining": 8}, midAugust)
	alerts := AlertsFor(states)
	last := alerts[len(alerts)-1]
	if last.Threshold != 100 {
		t.Fatalf("expected a 100%% alert, got %+v", last)
	}
	if want := "1,200"; !contains(last.Body, want) {
		t.Errorf("body should state the ₹%s overspend, got %q", want, last.Body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
