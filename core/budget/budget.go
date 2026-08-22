// Package budget evaluates spending against monthly envelopes and decides when
// an alert is worth interrupting someone for.
//
// The hard part of a budgeting app is not arithmetic, it is restraint. A
// notification that fires on every transaction gets swiped away and then
// silenced, at which point the app has failed. This package fires at most once
// per threshold per category per month, and it warns about a projected overrun
// early enough to be actionable rather than after the money is gone.
package budget

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

// Thresholds are the percentages of an envelope at which an alert fires.
// Crossing 50% mid-month is informative; 80% is a nudge; 100% is a fact.
var Thresholds = []int{50, 80, 100}

const (
	StatusOK            = "ok"
	StatusWarn          = "warn"
	StatusOver          = "over"
	StatusProjectedOver = "projected_over"
)

// WarnPct is the share of an envelope at which the state turns to warn.
const WarnPct = 80.0

// Evaluate computes the state of each envelope for the month containing now.
//
// Categories with spending but no envelope are returned with LimitMinor zero
// and status ok, so the UI can show where money went even where no budget was
// set. Envelopes with no spending are returned too, so a budget does not
// silently vanish from the list.
func Evaluate(envelopes []model.Envelope, spent map[string]int64, counts map[string]int, now time.Time) []model.EnvelopeState {
	elapsed, total := monthProgress(now)

	seen := map[string]bool{}
	out := make([]model.EnvelopeState, 0, len(envelopes)+len(spent))

	for _, e := range envelopes {
		seen[e.Category] = true
		out = append(out, stateFor(e.Category, e.LimitMinor, spent[e.Category], counts[e.Category], elapsed, total))
	}
	for cat, amt := range spent {
		if seen[cat] {
			continue
		}
		out = append(out, stateFor(cat, 0, amt, counts[cat], elapsed, total))
	}

	// Budgeted categories first, then by how close to the limit, so the list
	// leads with what needs attention.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.LimitMinor > 0) != (b.LimitMinor > 0) {
			return a.LimitMinor > 0
		}
		if a.Pct != b.Pct {
			return a.Pct > b.Pct
		}
		return a.Category < b.Category
	})
	return out
}

func stateFor(category string, limit, spent int64, count int, elapsedDays, totalDays float64) model.EnvelopeState {
	st := model.EnvelopeState{
		Category:   category,
		LimitMinor: limit,
		SpentMinor: spent,
		TxnCount:   count,
		Status:     StatusOK,
	}
	if limit > 0 {
		st.Pct = round1(float64(spent) / float64(limit) * 100)
	}

	// Straight-line projection. It is deliberately naive: a smarter model would
	// need per-category seasonality that a first-month user has no data for,
	// and a wrong sophisticated number is worse than an obvious rough one.
	if elapsedDays > 0 {
		st.ProjectedMinor = int64(float64(spent) / elapsedDays * totalDays)
	} else {
		st.ProjectedMinor = spent
	}

	switch {
	case limit <= 0:
		st.Status = StatusOK
	case spent >= limit:
		st.Status = StatusOver
	case st.Pct >= WarnPct:
		st.Status = StatusWarn
	case st.ProjectedMinor > limit:
		st.Status = StatusProjectedOver
	}
	return st
}

// monthProgress returns days elapsed (including the current partial day) and
// total days in the month.
func monthProgress(now time.Time) (elapsed, total float64) {
	loc := now.Location()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	next := start.AddDate(0, 1, 0)
	total = next.Sub(start).Hours() / 24
	elapsed = now.Sub(start).Hours() / 24
	if elapsed < 0.5 {
		// Guard the first hours of the month, where a straight-line projection
		// would multiply a single coffee into a five-figure forecast.
		elapsed = 0.5
	}
	if elapsed > total {
		elapsed = total
	}
	return elapsed, total
}

// Alert is a notification the app should deliver.
type Alert struct {
	Category  string `json:"category"`
	Threshold int    `json:"threshold"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
}

// AlertsFor returns the threshold crossings implied by the current state.
//
// It reports every threshold the spend has passed; the caller is responsible
// for suppressing ones already delivered, via the ledger's MarkAlertFired,
// which makes "fire once per crossing" durable across app restarts.
func AlertsFor(states []model.EnvelopeState) []Alert {
	var out []Alert
	for _, s := range states {
		if s.LimitMinor <= 0 {
			continue
		}
		for _, th := range Thresholds {
			if s.Pct < float64(th) {
				continue
			}
			out = append(out, Alert{
				Category:  s.Category,
				Threshold: th,
				Status:    s.Status,
				Title:     alertTitle(s, th),
				Body:      alertBody(s),
			})
		}
	}
	return out
}

func alertTitle(s model.EnvelopeState, th int) string {
	if th >= 100 {
		return fmt.Sprintf("%s budget spent", pretty(s.Category))
	}
	return fmt.Sprintf("%d%% of %s budget used", th, pretty(s.Category))
}

func alertBody(s model.EnvelopeState) string {
	remaining := s.LimitMinor - s.SpentMinor
	if remaining < 0 {
		return fmt.Sprintf("₹%s over your ₹%s limit, across %d transactions.",
			money(-remaining), money(s.LimitMinor), s.TxnCount)
	}
	return fmt.Sprintf("₹%s of ₹%s used, ₹%s left. On track for ₹%s by month end.",
		money(s.SpentMinor), money(s.LimitMinor), money(remaining), money(s.ProjectedMinor))
}

// money formats minor units in the Indian grouping convention (2,50,000)
// rather than the western one (250,000).
func money(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := minor / 100
	frac := minor % 100

	s := fmt.Sprintf("%d", whole)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if frac != 0 {
		s = fmt.Sprintf("%s.%02d", s, frac)
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Money is the exported formatter, shared with the CLI and the mobile binding
// so every surface renders amounts identically.
func Money(minor int64) string { return money(minor) }

// pretty turns a category slug into a label: "food_delivery" -> "Food delivery".
func pretty(category string) string {
	s := strings.ReplaceAll(category, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
