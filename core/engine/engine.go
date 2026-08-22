// Package engine wires the parser, rule engine, ledger and budget evaluator
// into the single object every front end talks to.
//
// Both the Android app and the desktop CLI drive this same type. Keeping the
// orchestration here rather than in either UI means the two cannot drift: a
// transaction booked on the phone and the same transaction imported on a laptop
// take an identical path and land in an identical row.
package engine

import (
	"fmt"
	"os"
	"time"

	"github.com/nandinisharma3120/khata/core/budget"
	"github.com/nandinisharma3120/khata/core/ledger"
	"github.com/nandinisharma3120/khata/core/model"
	"github.com/nandinisharma3120/khata/core/parse"
	"github.com/nandinisharma3120/khata/core/rules"
)

// Engine owns the ledger connection and the compiled rule set.
type Engine struct {
	store *ledger.Store
	rules *rules.Engine
	loc   *time.Location
}

// Options configures engine construction.
type Options struct {
	// LedgerPath is the append-only ledger file. Use ":memory:" for tests.
	LedgerPath string
	// ExtraPackPaths are additional rule packs layered over the builtin one,
	// typically the pack synthesised from this user's corrections.
	ExtraPackPaths []string
	// Location is the timezone used for month boundaries. Budget periods are a
	// human concept and must follow the user's calendar, not UTC.
	Location *time.Location
}

// New opens the ledger and compiles the rule set.
func New(opts Options) (*Engine, error) {
	if opts.LedgerPath == "" {
		return nil, fmt.Errorf("engine: LedgerPath is required")
	}
	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}

	store, err := ledger.Open(opts.LedgerPath)
	if err != nil {
		return nil, err
	}

	var extra []*rules.Pack
	for _, p := range opts.ExtraPackPaths {
		f, err := os.Open(p)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("open rule pack %s: %w", p, err)
		}
		pack, err := rules.LoadPack(f)
		f.Close()
		if err != nil {
			store.Close()
			return nil, err
		}
		extra = append(extra, pack)
	}

	re, err := rules.NewDefaultEngine(extra...)
	if err != nil {
		store.Close()
		return nil, err
	}

	return &Engine{store: store, rules: re, loc: loc}, nil
}

func (e *Engine) Close() error { return e.store.Close() }

// IngestResult describes what happened to one captured message.
type IngestResult struct {
	Booked    bool               `json:"booked"`
	Duplicate bool               `json:"duplicate"`
	Reason    string             `json:"reason,omitempty"`
	Kind      string             `json:"kind"`
	Txn       *model.Transaction `json:"txn,omitempty"`
	Alerts    []budget.Alert     `json:"alerts,omitempty"`
	Decision  rules.Decision     `json:"decision"`
}

// Ingest is the hot path: it runs inside the Android notification callback, so
// it must be fast, allocation-light, and incapable of blocking on I/O beyond a
// single appended line.
//
// The sequence is parse, categorise, store, then evaluate budgets. Budget
// evaluation runs after the write so the alert reflects the transaction that
// triggered it.
func (e *Engine) Ingest(msg model.Message) (IngestResult, error) {
	res := parse.Parse(msg)
	if !res.Bookable {
		// Remember that we saw and skipped it, without retaining the body.
		_ = e.store.RecordIgnored(string(res.Kind), res.Reason, msg.Sender, msg.ReceivedAt)
		return IngestResult{Kind: string(res.Kind), Reason: res.Reason}, nil
	}

	txn := res.Txn
	decision := e.rules.Categorize(*txn)
	txn.Category = decision.Category
	txn.MatchedRule = decision.RuleID
	if decision.Matched {
		txn.CategorySrc = decision.Source
	} else {
		txn.CategorySrc = "unmatched"
		// An unrecognised counterparty is exactly the case where a human
		// decision is worth asking for, and where that answer becomes a rule.
		txn.NeedsReview = true
	}

	if err := e.store.Insert(*txn); err != nil {
		if err == ledger.ErrDuplicate {
			return IngestResult{Kind: string(res.Kind), Duplicate: true,
				Reason: "already recorded from another source", Txn: txn}, nil
		}
		return IngestResult{}, err
	}

	alerts, err := e.pendingAlerts(txn.OccurredAt)
	if err != nil {
		// A failure to compute alerts must not lose a booked transaction.
		return IngestResult{Booked: true, Kind: string(res.Kind), Txn: txn, Decision: decision}, nil
	}

	return IngestResult{
		Booked: true, Kind: string(res.Kind), Txn: txn,
		Alerts: alerts, Decision: decision,
	}, nil
}

// pendingAlerts returns threshold crossings that have not yet been delivered
// for the month containing at.
func (e *Engine) pendingAlerts(at time.Time) ([]budget.Alert, error) {
	states, err := e.MonthStates(at)
	if err != nil {
		return nil, err
	}
	period := at.In(e.loc).Format("2006-01")

	var fresh []budget.Alert
	for _, a := range budget.AlertsFor(states) {
		first, err := e.store.MarkAlertFired(period, a.Category, a.Threshold)
		if err != nil {
			return nil, err
		}
		if first {
			fresh = append(fresh, a)
		}
	}
	return fresh, nil
}

// MonthStates evaluates every envelope for the month containing at.
func (e *Engine) MonthStates(at time.Time) ([]model.EnvelopeState, error) {
	at = at.In(e.loc)
	start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, e.loc)
	end := start.AddDate(0, 1, 0)

	spent, counts, err := e.store.SpentByCategory(start, end)
	if err != nil {
		return nil, err
	}
	envs, err := e.store.Envelopes()
	if err != nil {
		return nil, err
	}

	// Evaluate as of "now" when looking at the current month, and as of month
	// end for a past one, so projections in history are not truncated.
	asOf := time.Now().In(e.loc)
	if asOf.Before(start) || !asOf.Before(end) {
		asOf = end.Add(-time.Second)
	}
	return budget.Evaluate(envs, spent, counts, asOf), nil
}

// Correct applies a user's category decision and records it as training signal.
func (e *Engine) Correct(txnID, category string) error {
	return e.store.SetCategory(txnID, category, "user")
}

// Month returns the transactions for a calendar month.
func (e *Engine) Month(year int, m time.Month) ([]model.Transaction, error) {
	return e.store.Month(year, m, e.loc)
}

// NeedsReview lists transactions awaiting a human decision.
func (e *Engine) NeedsReview(limit int) ([]model.Transaction, error) {
	return e.store.NeedsReview(limit)
}

// SetEnvelope creates or updates a monthly limit.
func (e *Engine) SetEnvelope(category string, limitMinor int64) error {
	return e.store.SetEnvelope(model.Envelope{Category: category, LimitMinor: limitMinor})
}

// Compact rewrites the ledger, collapsing superseded records.
func (e *Engine) Compact() error { return e.store.Compact() }

// Corrections returns the exportable correction log.
func (e *Engine) Corrections() ([]model.Correction, error) { return e.store.Corrections() }

// Categories lists every category the rule set can assign.
func (e *Engine) Categories() []string { return e.rules.Categories() }

// Count returns the number of stored transactions.
func (e *Engine) Count() (int, error) { return e.store.Count() }

// RuleCount reports how many rules are loaded, for the about screen.
func (e *Engine) RuleCount() int { return e.rules.Len() }

// Packs lists loaded rule pack identifiers.
func (e *Engine) Packs() []string { return e.rules.Packs() }
