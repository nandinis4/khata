package rules

import (
	"strings"
	"testing"

	"github.com/nandinisharma3120/khata/core/model"
	"github.com/nandinisharma3120/khata/core/parse"
)

func txn(merchant string, opts ...func(*model.Transaction)) model.Transaction {
	t := model.Transaction{
		MerchantRaw: merchant,
		MerchantKey: parse.NormalizeMerchant(merchant),
		Direction:   model.DirectionDebit,
		Channel:     model.ChannelUPI,
		AmountMinor: 50000,
		Currency:    "INR",
	}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func withChannel(c model.Channel) func(*model.Transaction) {
	return func(t *model.Transaction) { t.Channel = c }
}
func withDirection(d model.Direction) func(*model.Transaction) {
	return func(t *model.Transaction) { t.Direction = d }
}
func withAmount(minor int64) func(*model.Transaction) {
	return func(t *model.Transaction) { t.AmountMinor = minor }
}

func TestBuiltinPackLoadsAndCompiles(t *testing.T) {
	e, err := NewDefaultEngine()
	if err != nil {
		t.Fatalf("builtin pack failed to compile: %v", err)
	}
	if e.Len() < 30 {
		t.Errorf("expected a substantial builtin pack, got %d rules", e.Len())
	}
	if len(e.Categories()) < 10 {
		t.Errorf("expected a broad taxonomy, got %v", e.Categories())
	}
}

func TestBuiltinCategorisation(t *testing.T) {
	e, err := NewDefaultEngine()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		merchant string
		want     string
		opts     []func(*model.Transaction)
	}{
		{merchant: "SWIGGY", want: "food_delivery"},
		{merchant: "RAZ*BLUE TOKAI COFFEE BLR", want: "dining"},
		{merchant: "UBER INDIA", want: "transport"},
		{merchant: "AMAZON PAY INDIA PVT LTD", want: "shopping"},
		{merchant: "NETFLIX", want: "subscriptions"},
		{merchant: "ZERODHA BROKING", want: "investments"},
		{merchant: "BESCOM ELECTRICITY", want: "utilities"},
		{merchant: "APOLLO PHARMACY", want: "health"},
		{merchant: "BOOKMYSHOW", want: "entertainment"},
		{merchant: "INDIGO AIRLINES", want: "travel"},
		{merchant: "SALARY", want: "income", opts: []func(*model.Transaction){withDirection(model.DirectionCredit)}},
		{merchant: "", want: "cash", opts: []func(*model.Transaction){withChannel(model.ChannelATM)}},
	}

	for _, tc := range cases {
		t.Run(tc.merchant+"/"+tc.want, func(t *testing.T) {
			d := e.Categorize(txn(tc.merchant, tc.opts...))
			if d.Category != tc.want {
				t.Errorf("got %q (rule %q), want %q", d.Category, d.RuleID, tc.want)
			}
			if !d.Matched {
				t.Error("expected a match")
			}
			if d.RuleID == "" {
				t.Error("every decision must name the rule that produced it")
			}
		})
	}
}

// Instamart is served by Swiggy but is groceries, not a restaurant order.
// The pack encodes this with an explicit priority; if someone reorders the
// file this test catches the regression.
func TestSpecificRuleBeatsGeneralRule(t *testing.T) {
	e, err := NewDefaultEngine()
	if err != nil {
		t.Fatal(err)
	}
	d := e.Categorize(txn("SWIGGYINSTAMART*BLR"))
	if d.Category != "groceries" {
		t.Errorf("got %q via rule %q, want groceries", d.Category, d.RuleID)
	}
}

func TestUncategorizedFallsThroughCleanly(t *testing.T) {
	e, err := NewDefaultEngine()
	if err != nil {
		t.Fatal(err)
	}
	d := e.Categorize(txn("QWZX UNKNOWN VENDOR 9981", withChannel(model.ChannelCard)))
	if d.Matched {
		t.Errorf("unexpected match: %q via %q", d.Category, d.RuleID)
	}
	if d.Category != "uncategorized" {
		t.Errorf("got %q, want uncategorized", d.Category)
	}
}

// A synthesized rule from the user's own corrections must beat a builtin
// guess. This is the mechanism that makes the correction loop feel like the
// app is learning.
func TestSynthesizedRuleOverridesBuiltin(t *testing.T) {
	base, err := BuiltinPack()
	if err != nil {
		t.Fatal(err)
	}
	learned := &Pack{
		Name:    "synthesized",
		Version: "1",
		Rules: []Rule{{
			ID:       "syn.uber_is_commute",
			Category: "commute",
			Source:   SourceSynthesized,
			Priority: 20,
			Match:    Match{MerchantEquals: []string{"uber"}},
		}},
	}
	e, err := NewEngine(base, learned)
	if err != nil {
		t.Fatal(err)
	}
	d := e.Categorize(txn("UBER INDIA"))
	if d.Category != "commute" {
		t.Errorf("got %q via %q, want commute", d.Category, d.RuleID)
	}
	if d.Source != SourceSynthesized {
		t.Errorf("source: got %q want %q", d.Source, SourceSynthesized)
	}
}

// At equal priority, a rule with more constraints must win regardless of the
// order rules were loaded in. Load-order dependence would make the same
// correction behave differently on two devices.
func TestOrderingIsIndependentOfLoadOrder(t *testing.T) {
	broad := Rule{ID: "broad", Category: "shopping", Match: Match{MerchantContains: []string{"mart"}}}
	narrow := Rule{ID: "narrow", Category: "groceries", Match: Match{
		MerchantEquals: []string{"dmart"}, Channel: []string{"card"}, Direction: "debit",
	}}

	a, err := NewEngine(&Pack{Name: "a", Version: "1", Rules: []Rule{broad, narrow}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEngine(&Pack{Name: "b", Version: "1", Rules: []Rule{narrow, broad}})
	if err != nil {
		t.Fatal(err)
	}

	tx := txn("DMART", withChannel(model.ChannelCard))
	da, db := a.Categorize(tx), b.Categorize(tx)
	if da.RuleID != db.RuleID {
		t.Errorf("load order changed the outcome: %q vs %q", da.RuleID, db.RuleID)
	}
	if da.Category != "groceries" {
		t.Errorf("narrower rule should win, got %q via %q", da.Category, da.RuleID)
	}
}

func TestAmountBoundsAreRespected(t *testing.T) {
	e, err := NewEngine(&Pack{Name: "t", Version: "1", Rules: []Rule{{
		ID:       "small.tip",
		Category: "tips",
		Match:    Match{MerchantContains: []string{"chaiwala"}, AmountMaxMinor: 10000},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Categorize(txn("Sharma Chaiwala", withAmount(5000))); !d.Matched {
		t.Error("amount within bounds should match")
	}
	if d := e.Categorize(txn("Sharma Chaiwala", withAmount(500000))); d.Matched {
		t.Error("amount above max should not match")
	}
}

func TestRejectsMalformedPacks(t *testing.T) {
	cases := map[string]string{
		"empty match block": `{"name":"x","version":"1","rules":[{"id":"a","category":"c","match":{}}]}`,
		"missing category":  `{"name":"x","version":"1","rules":[{"id":"a","match":{"merchant_equals":["z"]}}]}`,
		"missing id":        `{"name":"x","version":"1","rules":[{"category":"c","match":{"merchant_equals":["z"]}}]}`,
		"bad regex":         `{"name":"x","version":"1","rules":[{"id":"a","category":"c","match":{"merchant_regex":"([unclosed"}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := LoadPack(strings.NewReader(body))
			if err != nil {
				return // rejected at decode time, also fine
			}
			if _, err := NewEngine(p); err == nil {
				t.Error("expected engine construction to fail")
			}
		})
	}
}

func TestRejectsDuplicateRuleIDs(t *testing.T) {
	r := Rule{ID: "dup", Category: "c", Match: Match{MerchantEquals: []string{"x"}}}
	_, err := NewEngine(
		&Pack{Name: "a", Version: "1", Rules: []Rule{r}},
		&Pack{Name: "b", Version: "1", Rules: []Rule{r}},
	)
	if err == nil {
		t.Fatal("expected duplicate rule id to be rejected")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error should name the offending id, got %v", err)
	}
}

func BenchmarkCategorize(b *testing.B) {
	e, err := NewDefaultEngine()
	if err != nil {
		b.Fatal(err)
	}
	tx := txn("RAZ*BLUE TOKAI COFFEE BLR")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.Categorize(tx)
	}
}

// A VPA handle arrives unspaced ("bluetokai") while the card terminal spells
// the same merchant out ("BLUE TOKAI COFFEE"). One rule must cover both, or
// every rule has to enumerate spellings and most contributors will forget.
func TestSpacedRuleMatchesUnspacedVPAHandle(t *testing.T) {
	e, err := NewDefaultEngine()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"bluetokai@icici":           "dining",
		"RAZ*BLUE TOKAI COFFEE BLR": "dining",
		"apollopharmacy@axl":        "health",
		"APOLLO PHARMACY GURGAON":   "health",
		"swiggyinstamart@ybl":       "groceries",
	}
	for merchant, want := range cases {
		t.Run(merchant, func(t *testing.T) {
			d := e.Categorize(txn(merchant))
			if d.Category != want {
				t.Errorf("got %q via rule %q, want %q", d.Category, d.RuleID, want)
			}
		})
	}
}
