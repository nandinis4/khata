// Package parse turns raw bank SMS and payment-app notifications into
// structured transactions.
//
// The parser is deterministic and dependency-free by design. It runs entirely
// on the device, in the hot path of a notification callback, and its output is
// auditable: every field carries a reason it was extracted, and low-confidence
// parses are escalated to the user rather than guessed at.
package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

// ParserID identifies the extraction algorithm version. It is stamped onto
// every transaction so that a future parser change can be evaluated against
// records produced by the old one.
const ParserID = "heuristic-v1"

// Result is the outcome of parsing one message.
type Result struct {
	// Txn is non-nil only when Bookable is true.
	Txn *model.Transaction
	// Bookable reports whether this message represents a real money movement.
	Bookable bool
	// Kind is the classifier's verdict, retained even when not bookable so the
	// UI can explain why a message was ignored.
	Kind Kind
	// Reason is a short human-readable explanation for a non-bookable result.
	Reason string
}

// Parse extracts a transaction from a captured message.
//
// It never returns an error: an unparseable message is a normal, expected
// outcome, not an exceptional one. Callers inspect Bookable and Reason.
func Parse(msg model.Message) Result {
	text := strings.TrimSpace(msg.Title + ". " + msg.Body)
	text = collapseWhitespace(text)
	if text == "." || text == "" {
		return Result{Kind: KindUnrelated, Reason: "empty message"}
	}

	kind := Classify(text)
	if !kind.Bookable() {
		return Result{Kind: kind, Reason: reasonFor(kind)}
	}

	amount, unambiguous, ok := extractAmount(text)
	if !ok {
		return Result{Kind: kind, Reason: "no amount found"}
	}

	direction := extractDirection(text)
	if direction == model.DirectionUnknown {
		return Result{Kind: kind, Reason: "direction of money movement unclear"}
	}

	received := msg.ReceivedAt
	if received.IsZero() {
		received = time.Now()
	}

	merchantRaw := extractMerchant(text)
	occurredAt, hadDate := extractTime(text, received)
	account := extractAccount(text)
	reference := extractReference(text)
	channel := extractChannel(text)

	txn := &model.Transaction{
		OccurredAt:    occurredAt,
		AmountMinor:   amount,
		Currency:      "INR",
		Direction:     direction,
		Channel:       channel,
		MerchantRaw:   TitleizeMerchant(merchantRaw),
		MerchantKey:   NormalizeMerchant(merchantRaw),
		AccountHint:   account,
		Reference:     reference,
		Category:      "",
		CategorySrc:   "unset",
		SourcePackage: msg.SourcePackage,
		RawText:       text,
		ParserID:      ParserID,
		CapturedAt:    received,
	}

	txn.Confidence = score(txn, unambiguous, hadDate)
	txn.NeedsReview = txn.Confidence < model.ReviewThreshold
	txn.ID = transactionID(txn)

	return Result{Txn: txn, Bookable: true, Kind: kind}
}

// score computes the parser's self-assessed confidence.
//
// The weights reflect how much each field contributes to the record being
// trustworthy rather than how hard it was to extract. Amount and direction are
// load-bearing — a wrong value there corrupts the budget — so they dominate.
// A reference number is weighted highly because its presence also guarantees
// reliable deduplication.
func score(t *model.Transaction, amountUnambiguous, hadExplicitDate bool) float64 {
	s := 0.30 // a classified transaction with an amount and a direction

	if amountUnambiguous {
		s += 0.22
	} else {
		// Several plausible amounts; the message probably quotes a balance too
		// and we may have picked the wrong figure.
		s += 0.04
	}
	if t.MerchantKey != "" {
		s += 0.16
	}
	if t.Reference != "" {
		s += 0.12
	}
	if t.AccountHint != "" {
		s += 0.08
	}
	if t.Channel != model.ChannelUnknown {
		s += 0.07
	}
	if hadExplicitDate {
		s += 0.05
	}

	// Implausibly large amounts are usually a misparse of a concatenated
	// number; flag rather than trust.
	if t.AmountMinor > 100_000_000_00 {
		s -= 0.30
	}

	if s > 1 {
		s = 1
	}
	if s < 0 {
		s = 0
	}
	return round2(s)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// transactionID is a content-addressed identifier. Deriving it from the
// message content rather than a random source means re-importing the same SMS
// backup twice cannot create duplicate rows.
func transactionID(t *model.Transaction) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s",
		t.OccurredAt.UTC().Format(time.RFC3339),
		t.AmountMinor, t.Direction, t.MerchantKey, t.Reference, t.RawText)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func reasonFor(k Kind) string {
	switch k {
	case KindOTP:
		return "one-time password, not a transaction"
	case KindBalance:
		return "balance enquiry only"
	case KindDeclined:
		return "transaction did not succeed"
	case KindMandateNotice:
		return "advance notice of a future debit"
	case KindCollectReq:
		return "incoming payment request, no money moved yet"
	case KindPromotional:
		return "marketing message"
	}
	return "not a financial message"
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
