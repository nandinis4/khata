// Package ledger persists transactions to a single append-only file on the
// device.
//
// # Why not SQLite
//
// The obvious choice for on-device storage is SQLite, and for a multi-table
// app with ad-hoc queries it would be right. This ledger is a different shape:
// one person's transactions, a few thousand rows a year, always read in whole
// months, never joined against anything. For that workload an append-only log
// replayed into memory at startup is simpler, and it buys three things that
// matter to this project specifically.
//
// It keeps third-party code out of the path that touches financial data — the
// entire storage layer is auditable in one file with no driver, no cgo and no
// WASM runtime. It keeps the gomobile binary small, which matters when the
// engine ships inside an Android app. And the on-disk format is newline-
// delimited JSON, so a user can read their own ledger with `cat` and take it
// elsewhere without export tooling or trusting this program's honesty.
//
// The cost is real: every record lives in memory, and there is no query
// planner. At roughly 400 bytes a transaction, a decade of heavy spending is
// still under 30 MB, and Store is an interface — swapping in SQLite later is a
// contained change if the workload ever outgrows this.
//
// # Durability
//
// Appends are flushed and fsynced before returning, so a process kill cannot
// lose an acknowledged write. A partial trailing line from a power loss
// mid-write is detected and truncated at open: the log is valid up to the last
// complete record, and losing the interrupted one is correct, because it was
// never acknowledged.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

// DupWindow is how far apart two otherwise-identical captures can be and still
// be treated as the same payment.
//
// A bank SMS and the payment app's own push routinely arrive seconds apart, but
// a delayed SMS can lag by minutes. Two genuinely distinct payments of the same
// amount to the same merchant inside three minutes are rare; a missed duplicate
// is more annoying than a rare missed split payment, which the user can re-add.
const DupWindow = 3 * time.Minute

// ErrDuplicate reports that a transaction was already in the ledger.
var ErrDuplicate = errors.New("duplicate transaction")

// Event types in the log.
const (
	evTxn      = "txn"
	evCategory = "category"
	evEnvelope = "envelope"
	evAlert    = "alert"
	evIgnored  = "ignored"
)

// event is one line of the log. Exactly one payload field is set.
//
// Storing events rather than current state means a category correction is
// recorded as the correction it is, not as an overwrite. That history is what
// the toolchain compiles into rules, and it means no user action is silently
// destructive.
type event struct {
	Seq  int64     `json:"seq"`
	At   time.Time `json:"at"`
	Type string    `json:"type"`

	Txn      *model.Transaction `json:"txn,omitempty"`
	Category *categoryEvent     `json:"category,omitempty"`
	Envelope *model.Envelope    `json:"envelope,omitempty"`
	Alert    *alertEvent        `json:"alert,omitempty"`
	Ignored  *ignoredEvent      `json:"ignored,omitempty"`
}

// categoryEvent records a category change. It carries the previous value as
// well as the new one so that replaying the log is idempotent: whether a
// correction happened is a property of the event, not of whatever state the
// replay happens to have reached.
type categoryEvent struct {
	TransactionID string `json:"transaction_id"`
	From          string `json:"from"`
	To            string `json:"to"`
	Source        string `json:"source"`
}

type alertEvent struct {
	Period    string `json:"period"`
	Category  string `json:"category"`
	Threshold int    `json:"threshold"`
}

// ignoredEvent records that a message was seen and deliberately not booked.
// Only the classification is kept, never the message body: a user can audit
// what the app skipped without the app hoarding their SMS.
type ignoredEvent struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Sender string `json:"sender"`
}

// Store is the local ledger.
type Store struct {
	mu   sync.RWMutex
	path string
	f    *os.File
	w    *bufio.Writer
	seq  int64

	txns        map[string]*model.Transaction
	byDedup     map[string]string
	envelopes   map[string]model.Envelope
	alertsFired map[string]bool
	corrections []model.Correction
	ignored     int
}

// Open loads the ledger at path, creating it if necessary.
//
// Pass ":memory:" for an ephemeral store with no file backing, which is what
// the test suite uses.
func Open(path string) (*Store, error) {
	s := &Store{
		path:        path,
		txns:        map[string]*model.Transaction{},
		byDedup:     map[string]string{},
		envelopes:   map[string]model.Envelope{},
		alertsFired: map[string]bool{},
	}
	if path == ":memory:" {
		return s, nil
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create ledger dir: %w", err)
		}
	}
	if err := s.replay(); err != nil {
		return nil, err
	}

	// 0600: the ledger is readable only by the owning user.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	s.f = f
	s.w = bufio.NewWriter(f)
	return s, nil
}

// replay rebuilds in-memory state from the log, repairing a torn final line.
func (s *Store) replay() error {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ledger: %w", err)
	}
	defer f.Close()

	var lastGood int64
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && err == nil {
			var ev event
			if jsonErr := json.Unmarshal(line, &ev); jsonErr != nil {
				// A complete line that will not parse is corruption we should
				// not silently paper over.
				return fmt.Errorf("ledger corrupt at offset %d: %w", lastGood, jsonErr)
			}
			s.apply(ev)
			lastGood += int64(len(line))
			continue
		}
		if err == io.EOF {
			if len(line) > 0 {
				// Trailing bytes with no newline: an interrupted append. The
				// record was never acknowledged, so truncating is correct.
				if truncErr := os.Truncate(s.path, lastGood); truncErr != nil {
					return fmt.Errorf("repair torn ledger tail: %w", truncErr)
				}
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan ledger: %w", err)
		}
	}
}

func (s *Store) apply(ev event) {
	if ev.Seq > s.seq {
		s.seq = ev.Seq
	}
	switch ev.Type {
	case evTxn:
		if ev.Txn != nil {
			t := *ev.Txn
			s.txns[t.ID] = &t
			s.byDedup[t.DedupKey()] = t.ID
		}
	case evCategory:
		c := ev.Category
		if c == nil {
			return
		}
		t, ok := s.txns[c.TransactionID]
		if !ok {
			return
		}
		if c.Source == "user" && c.From != c.To {
			s.corrections = append(s.corrections, model.Correction{
				ID:            int64(len(s.corrections) + 1),
				TransactionID: t.ID,
				MerchantRaw:   t.MerchantRaw,
				MerchantKey:   t.MerchantKey,
				Channel:       t.Channel,
				FromCategory:  c.From,
				ToCategory:    c.To,
				CreatedAt:     ev.At,
			})
		}
		t.Category = c.To
		t.CategorySrc = c.Source
		t.NeedsReview = false
	case evEnvelope:
		if ev.Envelope != nil {
			s.envelopes[ev.Envelope.Category] = *ev.Envelope
		}
	case evAlert:
		if a := ev.Alert; a != nil {
			s.alertsFired[alertKey(a.Period, a.Category, a.Threshold)] = true
		}
	case evIgnored:
		s.ignored++
	}
}

func alertKey(period, category string, threshold int) string {
	return fmt.Sprintf("%s|%s|%d", period, category, threshold)
}

// append writes one event durably. The caller must hold the write lock.
func (s *Store) append(ev event) error {
	s.seq++
	ev.Seq = s.seq
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.apply(ev)

	if s.w == nil { // :memory:
		return nil
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("flush event: %w", err)
	}
	// fsync on every append. This is the slow, correct choice: a budgeting app
	// writes a handful of records a day, and losing an acknowledged
	// transaction to a battery pull is worse than a few milliseconds.
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sync ledger: %w", err)
	}
	return nil
}

// Close flushes and closes the log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return err
		}
	}
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}

// Insert stores a transaction, rejecting duplicates.
//
// Deduplication runs in two tiers. An exact dedup-key hit catches the same
// bank reference arriving from two apps. When no reference is available the
// fuzzy probe looks for a same-amount, same-direction, same-merchant entry
// inside DupWindow, which is the only way to catch a UPI payment reported once
// by the bank and once by the payment app where neither carried a reference.
func (s *Store) Insert(t model.Transaction) error {
	if t.ID == "" {
		return errors.New("transaction has no id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.txns[t.ID]; exists {
		return ErrDuplicate
	}
	if _, exists := s.byDedup[t.DedupKey()]; exists {
		return ErrDuplicate
	}
	if t.Reference == "" && s.fuzzyDuplicateLocked(t) {
		return ErrDuplicate
	}
	if t.Category == "" {
		t.Category = "uncategorized"
	}
	if t.Currency == "" {
		t.Currency = "INR"
	}
	return s.append(event{Type: evTxn, At: t.CapturedAt, Txn: &t})
}

func (s *Store) fuzzyDuplicateLocked(t model.Transaction) bool {
	lo, hi := t.OccurredAt.Add(-DupWindow), t.OccurredAt.Add(DupWindow)
	for _, ex := range s.txns {
		if ex.AmountMinor != t.AmountMinor || ex.Direction != t.Direction {
			continue
		}
		if ex.MerchantKey != t.MerchantKey {
			continue
		}
		if ex.OccurredAt.Before(lo) || ex.OccurredAt.After(hi) {
			continue
		}
		return true
	}
	return false
}

// SetCategory updates a transaction's category. When source is "user" and the
// category actually changed, the change is recorded as a correction — the only
// training signal khata collects.
func (s *Store) SetCategory(txnID, category, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.txns[txnID]
	if !ok {
		return fmt.Errorf("no transaction %s", txnID)
	}
	return s.append(event{Type: evCategory, Category: &categoryEvent{
		TransactionID: txnID, From: t.Category, To: category, Source: source,
	}})
}

// Between returns transactions in [from, to), newest first.
func (s *Store) Between(from, to time.Time) ([]model.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []model.Transaction
	for _, t := range s.txns {
		if t.OccurredAt.Before(from) || !t.OccurredAt.Before(to) {
			continue
		}
		out = append(out, *t)
	}
	sortNewestFirst(out)
	return out, nil
}

// Month returns every transaction in the given calendar month.
func (s *Store) Month(year int, m time.Month, loc *time.Location) ([]model.Transaction, error) {
	if loc == nil {
		loc = time.Local
	}
	start := time.Date(year, m, 1, 0, 0, 0, 0, loc)
	return s.Between(start, start.AddDate(0, 1, 0))
}

// NeedsReview returns transactions the engine was not confident enough to book
// silently, newest first.
func (s *Store) NeedsReview(limit int) ([]model.Transaction, error) {
	if limit <= 0 {
		limit = 50
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []model.Transaction
	for _, t := range s.txns {
		if t.NeedsReview || t.Category == "uncategorized" {
			out = append(out, *t)
		}
	}
	sortNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sortNewestFirst orders by time descending, breaking ties on id so the order
// is total and stable across runs.
func sortNewestFirst(ts []model.Transaction) {
	sort.Slice(ts, func(i, j int) bool {
		if !ts[i].OccurredAt.Equal(ts[j].OccurredAt) {
			return ts[i].OccurredAt.After(ts[j].OccurredAt)
		}
		return ts[i].ID < ts[j].ID
	})
}

// SpentByCategory totals net spend per category over a period.
//
// A refund is netted against the category it came back to, so a returned
// purchase does not leave a phantom hole in the budget. Income and transfers
// are excluded entirely: moving money between your own accounts is not
// spending, and counting it would double every rupee.
func (s *Store) SpentByCategory(from, to time.Time) (map[string]int64, map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totals := map[string]int64{}
	counts := map[string]int{}
	for _, t := range s.txns {
		if t.OccurredAt.Before(from) || !t.OccurredAt.Before(to) {
			continue
		}
		if t.Category == "income" || t.Category == "transfers" {
			continue
		}
		switch t.Direction {
		case model.DirectionDebit:
			totals[t.Category] += t.AmountMinor
		case model.DirectionCredit:
			totals[t.Category] -= t.AmountMinor
		default:
			continue
		}
		counts[t.Category]++
	}
	for c, v := range totals {
		if v < 0 {
			totals[c] = 0 // a category refunded net-negative shows as zero spend
		}
	}
	return totals, counts, nil
}

// SetEnvelope creates or replaces a monthly limit for a category.
func (s *Store) SetEnvelope(e model.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.append(event{Type: evEnvelope, Envelope: &e})
}

// Envelopes lists configured limits, sorted by category.
func (s *Store) Envelopes() ([]model.Envelope, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Envelope, 0, len(s.envelopes))
	for _, e := range s.envelopes {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out, nil
}

// MarkAlertFired records a threshold crossing and reports whether this call is
// the one that fired it, so an alert is delivered once per crossing rather
// than on every subsequent transaction.
func (s *Store) MarkAlertFired(period, category string, threshold int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alertsFired[alertKey(period, category, threshold)] {
		return false, nil
	}
	if err := s.append(event{Type: evAlert, Alert: &alertEvent{
		Period: period, Category: category, Threshold: threshold,
	}}); err != nil {
		return false, err
	}
	return true, nil
}

// Corrections returns every recorded user correction.
//
// This is the one dataset khata will hand over on request. It contains
// merchant names and categories, but no amounts, dates, account tails or
// message text. That asymmetry is deliberate: it is enough to compile better
// rules, and not enough to reconstruct anyone's spending.
func (s *Store) Corrections() ([]model.Correction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Correction(nil), s.corrections...), nil
}

// RecordIgnored notes that a message was seen and deliberately not booked.
func (s *Store) RecordIgnored(kind, reason, sender string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.append(event{Type: evIgnored, At: at, Ignored: &ignoredEvent{
		Kind: kind, Reason: reason, Sender: sender,
	}})
}

// Count returns the number of stored transactions.
func (s *Store) Count() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.txns), nil
}

// Compact rewrites the log with one event per current fact, discarding
// superseded category changes and fired-alert records for past months.
//
// It writes to a temporary file and renames it into place, so a crash during
// compaction leaves the original log intact.
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == ":memory:" {
		return nil
	}

	tmp := s.path + ".compact"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open compaction target: %w", err)
	}
	w := bufio.NewWriter(f)

	var seq int64
	write := func(ev event) error {
		seq++
		ev.Seq = seq
		if ev.At.IsZero() {
			ev.At = time.Now()
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		_, err = w.Write(append(line, '\n'))
		return err
	}

	ids := make([]string, 0, len(s.txns))
	for id := range s.txns {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		t := *s.txns[id]
		if err := write(event{Type: evTxn, At: t.CapturedAt, Txn: &t}); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	for _, e := range s.envelopes {
		e := e
		if err := write(event{Type: evEnvelope, Envelope: &e}); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	// Corrections are replayed as category events so the training signal
	// survives compaction. They are idempotent: the transaction above already
	// carries the corrected category, and the event records its own from/to.
	for _, c := range s.corrections {
		if err := write(event{Type: evCategory, At: c.CreatedAt, Category: &categoryEvent{
			TransactionID: c.TransactionID,
			From:          c.FromCategory,
			To:            c.ToCategory,
			Source:        "user",
		}}); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}

	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	if s.w != nil {
		s.w.Flush()
		s.f.Close()
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("swap compacted ledger: %w", err)
	}

	nf, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("reopen ledger after compaction: %w", err)
	}
	s.f = nf
	s.w = bufio.NewWriter(nf)
	s.seq = seq
	return nil
}
