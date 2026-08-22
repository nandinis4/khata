// Package model defines the value types shared across the khata engine.
//
// Everything here is deliberately dependency-free and JSON-serialisable so the
// same structures can cross the gomobile boundary into the Android app, be
// persisted by the ledger, and be emitted to the Python toolchain without
// translation layers.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Direction records which way money moved.
type Direction string

const (
	DirectionDebit   Direction = "debit"
	DirectionCredit  Direction = "credit"
	DirectionUnknown Direction = "unknown"
)

// Channel records the payment rail a transaction travelled over. Knowing the
// rail materially improves categorisation: an ATM withdrawal and a UPI payment
// to the same merchant string mean very different things.
type Channel string

const (
	ChannelUPI     Channel = "upi"
	ChannelCard    Channel = "card"
	ChannelATM     Channel = "atm"
	ChannelNetBank Channel = "netbanking"
	ChannelIMPS    Channel = "imps"
	ChannelNEFT    Channel = "neft"
	ChannelWallet  Channel = "wallet"
	ChannelAutoPay Channel = "autopay"
	ChannelCash    Channel = "cash"
	ChannelUnknown Channel = "unknown"
)

// Message is a raw capture from the device: an SMS, or an Android notification
// posted by a bank or payment app. The engine never sees anything richer than
// this, which keeps the capture surface auditable.
type Message struct {
	// SourcePackage is the Android package that posted the notification, or
	// "sms" for messages read from the SMS inbox.
	SourcePackage string `json:"source_package"`
	// Sender is the SMS short-code or notification channel, e.g. "AD-HDFCBK".
	Sender string `json:"sender"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	// ReceivedAt is device local time at capture.
	ReceivedAt time.Time `json:"received_at"`
}

// Transaction is the structured record the parser produces.
//
// Amounts are stored as integer minor units (paise for INR) to keep arithmetic
// exact; float money is a bug waiting to happen in a budgeting app.
type Transaction struct {
	ID          string    `json:"id"`
	OccurredAt  time.Time `json:"occurred_at"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Direction   Direction `json:"direction"`
	Channel     Channel   `json:"channel"`

	// MerchantRaw is the counterparty exactly as it appeared in the message.
	MerchantRaw string `json:"merchant_raw"`
	// MerchantKey is the normalised, matchable form used by the rule engine.
	MerchantKey string `json:"merchant_key"`

	// AccountHint is the masked account or card tail, e.g. "XX4412".
	AccountHint string `json:"account_hint"`
	// Reference is the bank/UPI reference number when present. It is the
	// strongest available dedup signal.
	Reference string `json:"reference"`

	Category    string `json:"category"`
	CategorySrc string `json:"category_source"`
	MatchedRule string `json:"matched_rule"`

	// Confidence is the parser's self-assessed extraction quality in [0,1].
	// Anything below ReviewThreshold is surfaced to the user for confirmation
	// rather than silently booked.
	Confidence float64 `json:"confidence"`

	SourcePackage string `json:"source_package"`
	RawText       string `json:"raw_text"`
	ParserID      string `json:"parser_id"`

	// NeedsReview is set when the engine wants a human decision, either
	// because extraction was shaky or because no rule claimed the merchant.
	NeedsReview bool `json:"needs_review"`

	CapturedAt time.Time `json:"captured_at"`
}

// ReviewThreshold is the confidence below which a parse is not trusted enough
// to book without confirmation.
const ReviewThreshold = 0.62

// Major returns the amount in major units, for display only. Never use the
// result for arithmetic that feeds back into the ledger.
func (t Transaction) Major() float64 {
	return float64(t.AmountMinor) / 100.0
}

// Signed returns the amount in minor units, negative for debits, so that a
// running balance is a plain sum.
func (t Transaction) Signed() int64 {
	if t.Direction == DirectionDebit {
		return -t.AmountMinor
	}
	return t.AmountMinor
}

// String renders a compact human-readable form used by the CLI and logs.
func (t Transaction) String() string {
	sign := "+"
	if t.Direction == DirectionDebit {
		sign = "-"
	}
	name := t.MerchantRaw
	if name == "" {
		name = "(unknown)"
	}
	return fmt.Sprintf("%s %s%.2f %s [%s/%s] %s",
		t.OccurredAt.Format("2006-01-02 15:04"),
		sign, t.Major(), t.Currency,
		t.Channel, t.Category, name)
}

// DedupKey produces a stable fingerprint used to collapse duplicate captures.
//
// The same payment routinely arrives twice: once as a bank SMS and once as a
// push from the payment app. When a bank reference is present it is globally
// unique and sufficient on its own. Without one we fall back to a fuzzy key
// that buckets by minute, which the ledger widens into a time window.
func (t Transaction) DedupKey() string {
	h := sha256.New()
	if t.Reference != "" {
		fmt.Fprintf(h, "ref|%s|%d|%s", strings.ToLower(t.Reference), t.AmountMinor, t.Direction)
	} else {
		fmt.Fprintf(h, "fuzzy|%d|%s|%s|%s",
			t.AmountMinor,
			t.Direction,
			t.MerchantKey,
			t.OccurredAt.UTC().Format("2006-01-02T15:04"))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Envelope is a monthly spending limit for one category.
type Envelope struct {
	Category   string `json:"category"`
	LimitMinor int64  `json:"limit_minor"`
	// Rollover carries unspent budget into the next month.
	Rollover bool `json:"rollover"`
}

// EnvelopeState is the evaluated status of an envelope for a given month.
type EnvelopeState struct {
	Category   string  `json:"category"`
	LimitMinor int64   `json:"limit_minor"`
	SpentMinor int64   `json:"spent_minor"`
	Pct        float64 `json:"pct"`
	// ProjectedMinor extrapolates current burn rate to month end.
	ProjectedMinor int64  `json:"projected_minor"`
	Status         string `json:"status"` // ok | warn | over | projected_over
	TxnCount       int    `json:"txn_count"`
}

// Correction is a user overriding the engine's category choice. Corrections are
// the training signal the Python toolchain compiles into new deterministic
// rules; they are the only data a user can choose to export.
type Correction struct {
	ID            int64     `json:"id"`
	TransactionID string    `json:"transaction_id"`
	MerchantRaw   string    `json:"merchant_raw"`
	MerchantKey   string    `json:"merchant_key"`
	Channel       Channel   `json:"channel"`
	FromCategory  string    `json:"from_category"`
	ToCategory    string    `json:"to_category"`
	CreatedAt     time.Time `json:"created_at"`
}
