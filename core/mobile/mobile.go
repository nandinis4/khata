// Package mobile is the gomobile binding surface for the khata engine.
//
// gomobile only bridges a narrow set of types across the JNI boundary: signed
// integers, floats, bools, strings, []byte, and structs defined in the bound
// package. Maps, slices of structs and time.Time do not cross. Rather than
// mirror every model type into a bindable shape — which would double the
// surface area and guarantee drift — this package exchanges JSON strings for
// anything structured and keeps scalars scalar.
//
// The Kotlin side deserialises with kotlinx.serialization into data classes
// that mirror core/model. One engine, one set of semantics, one place where
// behaviour is defined.
package mobile

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nandinisharma3120/khata/core/budget"
	"github.com/nandinisharma3120/khata/core/engine"
	"github.com/nandinisharma3120/khata/core/model"
)

// Version identifies the engine build to the about screen.
const version = "0.1.0"

// Version returns the engine version string.
func Version() string { return version }

// Khata is the handle the Android app holds for the lifetime of the process.
type Khata struct {
	eng *engine.Engine
}

// Open initialises the engine against a ledger file.
//
// On Android, pass a path inside the app's private files directory. That
// directory is not world-readable and is excluded from cloud backup by the
// manifest, which together mean the ledger never leaves the device unless the
// user exports it deliberately.
func Open(ledgerPath string) (*Khata, error) {
	e, err := engine.New(engine.Options{
		LedgerPath: ledgerPath,
		Location:   time.Local,
	})
	if err != nil {
		return nil, err
	}
	return &Khata{eng: e}, nil
}

// OpenWithRulePack initialises the engine with an extra rule pack layered over
// the builtin one — normally the pack synthesised from this user's own
// corrections.
func OpenWithRulePack(ledgerPath, packPath string) (*Khata, error) {
	e, err := engine.New(engine.Options{
		LedgerPath:     ledgerPath,
		ExtraPackPaths: []string{packPath},
		Location:       time.Local,
	})
	if err != nil {
		return nil, err
	}
	return &Khata{eng: e}, nil
}

// Close flushes and releases the ledger.
func (k *Khata) Close() error { return k.eng.Close() }

// Ingest handles one captured notification or SMS and returns the result as
// JSON matching engine.IngestResult.
//
// This runs on the binder thread inside NotificationListenerService.
// onNotificationPosted, so it must return promptly: the work is a regex pass,
// a rule scan and one appended line.
func (k *Khata) Ingest(sourcePackage, sender, title, body string, receivedAtUnixMillis int64) (string, error) {
	received := time.Now()
	if receivedAtUnixMillis > 0 {
		received = time.UnixMilli(receivedAtUnixMillis)
	}
	res, err := k.eng.Ingest(model.Message{
		SourcePackage: sourcePackage,
		Sender:        sender,
		Title:         title,
		Body:          body,
		ReceivedAt:    received,
	})
	if err != nil {
		return "", err
	}
	return toJSON(res)
}

// MonthStates returns budget envelope states for a month as a JSON array.
// Month is 1-12.
func (k *Khata) MonthStates(year, month int) (string, error) {
	if month < 1 || month > 12 {
		return "", fmt.Errorf("month out of range: %d", month)
	}
	at := time.Date(year, time.Month(month), 1, 12, 0, 0, 0, time.Local)
	states, err := k.eng.MonthStates(at)
	if err != nil {
		return "", err
	}
	return toJSON(states)
}

// Transactions returns a month's transactions as a JSON array, newest first.
func (k *Khata) Transactions(year, month int) (string, error) {
	if month < 1 || month > 12 {
		return "", fmt.Errorf("month out of range: %d", month)
	}
	txns, err := k.eng.Month(year, time.Month(month))
	if err != nil {
		return "", err
	}
	return toJSON(txns)
}

// NeedsReview returns transactions awaiting a category decision, as JSON.
// These are what the app surfaces as actionable notifications.
func (k *Khata) NeedsReview(limit int) (string, error) {
	txns, err := k.eng.NeedsReview(limit)
	if err != nil {
		return "", err
	}
	return toJSON(txns)
}

// Correct applies the user's category choice and records it as training signal.
func (k *Khata) Correct(transactionID, category string) error {
	return k.eng.Correct(transactionID, category)
}

// SetEnvelope sets a monthly limit for a category, in minor units (paise).
func (k *Khata) SetEnvelope(category string, limitMinor int64) error {
	return k.eng.SetEnvelope(category, limitMinor)
}

// Categories returns every assignable category as a JSON array of strings,
// so the correction picker does not hardcode a taxonomy the engine owns.
func (k *Khata) Categories() (string, error) { return toJSON(k.eng.Categories()) }

// Corrections returns the exportable correction log as JSON. This is the only
// data khata will hand out, and only when the user asks for it.
func (k *Khata) Corrections() (string, error) {
	c, err := k.eng.Corrections()
	if err != nil {
		return "", err
	}
	return toJSON(c)
}

// Count returns the number of stored transactions.
func (k *Khata) Count() (int, error) { return k.eng.Count() }

// RuleCount returns the number of loaded rules, for the about screen.
func (k *Khata) RuleCount() int { return k.eng.RuleCount() }

// FormatMoney renders minor units using Indian digit grouping, so Kotlin and
// Go never disagree about how an amount looks.
func FormatMoney(minor int64) string { return budget.Money(minor) }

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(b), nil
}
