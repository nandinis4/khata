// Package rules implements khata's deterministic categorisation engine.
//
// Categorisation deliberately does not call a language model at runtime. A
// model in the hot path would mean either shipping transaction text off the
// device — which the project exists to avoid — or bundling a local model large
// enough to hurt install size and battery. Instead, the LLM runs offline in the
// Python toolchain and compiles user corrections into rule packs: plain data,
// reviewable in a diff, identical on every device, and fast enough to run
// inside a notification callback.
//
// The model is a compiler, not an oracle.
package rules

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/nandinisharma3120/khata/core/model"
)

// Source records where a rule came from. It drives both tie-breaking and the
// explanation shown to the user.
const (
	SourceBuiltin     = "builtin"     // shipped with the app
	SourceSynthesized = "synthesized" // compiled by the toolchain from corrections
	SourceUser        = "user"        // written by hand
)

// Match is the predicate half of a rule. An empty field is not a constraint;
// all populated fields must hold for the rule to fire.
type Match struct {
	// MerchantEquals compares against the normalised merchant key.
	MerchantEquals []string `json:"merchant_equals,omitempty"`
	// MerchantContains matches if any listed substring occurs in the key.
	MerchantContains []string `json:"merchant_contains,omitempty"`
	// MerchantRegex is the escape hatch; prefer the simpler forms.
	MerchantRegex string `json:"merchant_regex,omitempty"`

	Channel       []string `json:"channel,omitempty"`
	Direction     string   `json:"direction,omitempty"`
	SourcePackage []string `json:"source_package,omitempty"`

	AmountMinMinor int64 `json:"amount_min_minor,omitempty"`
	AmountMaxMinor int64 `json:"amount_max_minor,omitempty"`
}

// Rule maps a predicate to a category.
type Rule struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Match    Match  `json:"match"`
	// Priority breaks ties; higher wins. Leave at 0 unless a rule must
	// deliberately override a more specific one.
	Priority int    `json:"priority,omitempty"`
	Source   string `json:"source,omitempty"`
	Note     string `json:"note,omitempty"`

	compiled    *regexp.Regexp
	specificity int
}

// Pack is a versioned, distributable set of rules.
type Pack struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// Engine matches transactions against a compiled, ordered rule set.
type Engine struct {
	rules []Rule
	packs []string
}

// Decision explains a categorisation. Every categorised transaction can answer
// "why?", which is what makes a correction meaningful: the user is disagreeing
// with a specific, named rule.
type Decision struct {
	Category string `json:"category"`
	RuleID   string `json:"rule_id"`
	Source   string `json:"source"`
	Matched  bool   `json:"matched"`
}

// LoadPack parses a rule pack from JSON.
func LoadPack(r io.Reader) (*Pack, error) {
	var p Pack
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("decode rule pack: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("rule pack has no name")
	}
	return &p, nil
}

// NewEngine compiles one or more packs into a matcher.
//
// Compilation happens once at startup so the per-transaction path does no
// regex compilation and no allocation beyond the decision itself.
func NewEngine(packs ...*Pack) (*Engine, error) {
	e := &Engine{}
	seen := map[string]string{}

	for _, p := range packs {
		if p == nil {
			continue
		}
		e.packs = append(e.packs, p.Name+"@"+p.Version)
		for _, r := range p.Rules {
			if r.ID == "" {
				return nil, fmt.Errorf("pack %q: rule with empty id", p.Name)
			}
			if prev, dup := seen[r.ID]; dup {
				return nil, fmt.Errorf("duplicate rule id %q (already in pack %q)", r.ID, prev)
			}
			seen[r.ID] = p.Name

			if r.Category == "" {
				return nil, fmt.Errorf("rule %q: empty category", r.ID)
			}
			if r.Source == "" {
				r.Source = SourceBuiltin
			}
			if r.Match.MerchantRegex != "" {
				re, err := regexp.Compile("(?i)" + r.Match.MerchantRegex)
				if err != nil {
					return nil, fmt.Errorf("rule %q: bad merchant_regex: %w", r.ID, err)
				}
				r.compiled = re
			}
			// Normalise the literal forms once, so matching is a plain
			// comparison against an already-normalised transaction key.
			for i, s := range r.Match.MerchantEquals {
				r.Match.MerchantEquals[i] = strings.ToLower(strings.TrimSpace(s))
			}
			for i, s := range r.Match.MerchantContains {
				r.Match.MerchantContains[i] = strings.ToLower(strings.TrimSpace(s))
			}
			r.specificity = specificityOf(r.Match)
			if r.specificity == 0 {
				return nil, fmt.Errorf("rule %q: match block is empty, it would claim every transaction", r.ID)
			}
			e.rules = append(e.rules, r)
		}
	}

	// Order once: priority, then specificity, then provenance, then id.
	// Sorting up front makes matching a linear scan that can stop at the first
	// hit, and makes the outcome independent of pack load order.
	sort.SliceStable(e.rules, func(i, j int) bool {
		a, b := e.rules[i], e.rules[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.specificity != b.specificity {
			return a.specificity > b.specificity
		}
		if ra, rb := sourceRank(a.Source), sourceRank(b.Source); ra != rb {
			return ra > rb
		}
		return a.ID < b.ID
	})

	return e, nil
}

// sourceRank encodes trust: a rule the user wrote or corrected beats a guess
// the toolchain synthesised, which beats a generic builtin.
func sourceRank(s string) int {
	switch s {
	case SourceUser:
		return 3
	case SourceSynthesized:
		return 2
	default:
		return 1
	}
}

// specificityOf counts constraints so that a narrower rule wins over a broader
// one at equal priority.
func specificityOf(m Match) int {
	n := 0
	// Exact merchant matches are the strongest signal, weighted accordingly.
	if len(m.MerchantEquals) > 0 {
		n += 4
	}
	if len(m.MerchantContains) > 0 {
		n += 2
	}
	if m.MerchantRegex != "" {
		n += 2
	}
	if len(m.Channel) > 0 {
		n++
	}
	if m.Direction != "" {
		n++
	}
	if len(m.SourcePackage) > 0 {
		n++
	}
	if m.AmountMinMinor > 0 || m.AmountMaxMinor > 0 {
		n++
	}
	return n
}

// Categorize returns the first matching rule's category.
//
// The scan is ordered, so "first match" means "highest priority, most specific,
// most trusted" — not "first in file".
func (e *Engine) Categorize(t model.Transaction) Decision {
	for i := range e.rules {
		if e.rules[i].matches(t) {
			r := e.rules[i]
			return Decision{Category: r.Category, RuleID: r.ID, Source: r.Source, Matched: true}
		}
	}
	return Decision{Category: "uncategorized", Matched: false}
}

func (r *Rule) matches(t model.Transaction) bool {
	m := &r.Match

	if m.Direction != "" && string(t.Direction) != m.Direction {
		return false
	}
	if len(m.Channel) > 0 && !containsFold(m.Channel, string(t.Channel)) {
		return false
	}
	if len(m.SourcePackage) > 0 && !containsFold(m.SourcePackage, t.SourcePackage) {
		return false
	}
	if m.AmountMinMinor > 0 && t.AmountMinor < m.AmountMinMinor {
		return false
	}
	if m.AmountMaxMinor > 0 && t.AmountMinor > m.AmountMaxMinor {
		return false
	}

	// Merchant predicates are OR-ed with each other: a rule may list several
	// spellings of the same counterparty.
	hasMerchantPredicate := len(m.MerchantEquals) > 0 || len(m.MerchantContains) > 0 || r.compiled != nil
	if !hasMerchantPredicate {
		return true
	}
	key := t.MerchantKey
	if key == "" {
		return false
	}
	// The same counterparty reaches us spaced ("blue tokai coffee" from a card
	// terminal) and unspaced ("bluetokai" from a VPA handle). Comparing the
	// despaced forms as well means one rule covers both, instead of every rule
	// needing to enumerate the spellings.
	keyTight := despace(key)
	for _, s := range m.MerchantEquals {
		if key == s || keyTight == despace(s) {
			return true
		}
	}
	for _, s := range m.MerchantContains {
		if s == "" {
			continue
		}
		if strings.Contains(key, s) || strings.Contains(keyTight, despace(s)) {
			return true
		}
	}
	if r.compiled != nil && r.compiled.MatchString(key) {
		return true
	}
	return false
}

// despace removes spaces so that spaced and unspaced spellings of the same
// merchant compare equal.
func despace(s string) string { return strings.ReplaceAll(s, " ", "") }

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// Len reports the number of compiled rules.
func (e *Engine) Len() int { return len(e.rules) }

// Packs lists the loaded pack identifiers, for display in an about screen.
func (e *Engine) Packs() []string { return append([]string(nil), e.packs...) }

// Categories returns every category the engine can assign, sorted. Useful for
// populating a correction picker without hardcoding a taxonomy in the UI.
func (e *Engine) Categories() []string {
	set := map[string]bool{}
	for _, r := range e.rules {
		set[r.Category] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
