package parse

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nandinisharma3120/khata/core/model"
)

// ---------------------------------------------------------------------------
// Amount
// ---------------------------------------------------------------------------

var (
	// Currency-led and currency-trailing amount forms.
	reAmountLed   = regexp.MustCompile(`(?i)(?:inr|rs\.?|₹)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	reAmountTrail = regexp.MustCompile(`(?i)([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*(?:inr|rs\b\.?|₹)`)
	// SBI and a few others omit the currency entirely: "debited by 150.0 on".
	reAmountBare = regexp.MustCompile(`(?i)\b(?:debited|credited)\s+by\s+([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)

	// Context that marks an amount as a balance or a limit rather than the
	// transaction value.
	reBalanceCtx = regexp.MustCompile(`(?i)(avl\s*bal|available\s+bal|avbl\s*bal|closing\s+bal|avl\s*lmt|available\s+limit|credit\s+limit|bal(ance)?\s*[:.]?\s*$|total\s+due|min(imum)?\s+due|outstanding)`)
	// Context that marks an amount as the transaction value.
	reTxnCtx = regexp.MustCompile(`(?i)(debited|credited|spent|paid|sent|withdrawn|received|purchase|deducted|txn|transaction|for)`)
)

type amountCandidate struct {
	minor int64
	pos   int
	score float64
}

// extractAmount picks the transaction value out of a message that usually
// contains several money figures.
//
// A typical debit alert carries both the amount spent and the balance
// remaining; naively taking the first or largest match books the wrong number.
// Each candidate is scored on the words immediately around it: balance and
// limit context pushes a candidate down, transaction verbs pull it up. The
// returned bool reports whether the winner was clearly separated from the
// runner-up, which feeds the overall parse confidence.
func extractAmount(text string) (minor int64, unambiguous bool, ok bool) {
	var cands []amountCandidate

	collect := func(re *regexp.Regexp, base float64) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			raw := text[m[2]:m[3]]
			v, err := parseMoney(raw)
			if err != nil || v <= 0 {
				continue
			}
			cands = append(cands, amountCandidate{minor: v, pos: m[0], score: base})
		}
	}
	collect(reAmountLed, 1.0)
	collect(reAmountTrail, 0.9)
	collect(reAmountBare, 1.0)

	if len(cands) == 0 {
		return 0, false, false
	}

	// De-duplicate overlapping matches at the same position.
	seen := map[int]bool{}
	uniq := cands[:0]
	for _, c := range cands {
		if seen[c.pos] {
			continue
		}
		seen[c.pos] = true
		uniq = append(uniq, c)
	}
	cands = uniq

	for i := range cands {
		c := &cands[i]
		before := window(text, c.pos-42, c.pos)
		after := window(text, c.pos, c.pos+28)

		if reBalanceCtx.MatchString(before) {
			c.score -= 1.4
		}
		if reBalanceCtx.MatchString(after) {
			// "Rs.23,110.42 is your available balance"
			c.score -= 0.7
		}
		if reTxnCtx.MatchString(before) {
			c.score += 0.6
		}
		if reTxnCtx.MatchString(after) {
			c.score += 0.35
		}
		// Earlier amounts are more often the transaction value.
		c.score -= float64(c.pos) / float64(len(text)+1) * 0.3
	}

	best, second := -1, -1
	for i := range cands {
		if best < 0 || cands[i].score > cands[best].score {
			second, best = best, i
		} else if second < 0 || cands[i].score > cands[second].score {
			second = i
		}
	}

	unambiguous = true
	if second >= 0 && math.Abs(cands[best].score-cands[second].score) < 0.35 {
		unambiguous = false
	}
	return cands[best].minor, unambiguous, true
}

// parseMoney converts "1,299.50" into 129950 minor units without ever touching
// a float, so no rounding error can enter the ledger.
func parseMoney(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, err
	}
	switch len(fracPart) {
	case 0:
		fracPart = "00"
	case 1:
		fracPart += "0"
	default:
		fracPart = fracPart[:2] // banks occasionally emit extra precision
	}
	frac, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, err
	}
	return whole*100 + frac, nil
}

func window(s string, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	if lo > hi {
		return ""
	}
	return s[lo:hi]
}

// ---------------------------------------------------------------------------
// Direction
// ---------------------------------------------------------------------------

// extractDirection resolves debit vs credit by earliest verb.
//
// Some issuers describe both sides of the same movement — ICICI writes
// "Acct XX1 debited for Rs 1,299; AMAZON credited" — so the verb that appears
// first is the one describing the user's own account.
func extractDirection(text string) model.Direction {
	d := reDebitVerb.FindStringIndex(text)
	c := reCredVerb.FindStringIndex(text)
	switch {
	case d == nil && c == nil:
		return model.DirectionUnknown
	case d == nil:
		return model.DirectionCredit
	case c == nil:
		return model.DirectionDebit
	case d[0] <= c[0]:
		return model.DirectionDebit
	default:
		return model.DirectionCredit
	}
}

// ---------------------------------------------------------------------------
// Channel
// ---------------------------------------------------------------------------

var (
	reUPI     = regexp.MustCompile(`(?i)\b(upi|vpa)\b|[a-z0-9._-]+@(ybl|okaxis|okhdfcbank|oksbi|okicici|paytm|apl|axl|ibl|upi|jupiteraxis|fam|superyes|yesg|abfspay)\b`)
	reCard    = regexp.MustCompile(`(?i)\b(card|debit\s?card|credit\s?card|cc\s+no)\b`)
	reATM     = regexp.MustCompile(`(?i)\b(atm|cash\s+withdrawal|withdrawn\s+from)\b`)
	reIMPS    = regexp.MustCompile(`(?i)\bimps\b`)
	reNEFT    = regexp.MustCompile(`(?i)\b(neft|rtgs)\b`)
	reWallet  = regexp.MustCompile(`(?i)\b(wallet|paytm\s+wallet|amazon\s+pay\s+balance)\b`)
	reAutoPay = regexp.MustCompile(`(?i)\b(autopay|mandate|standing\s+instruction|e-?nach)\b`)
)

func extractChannel(text string) model.Channel {
	// Most specific rails first; UPI and card markers frequently co-occur
	// because a UPI payment is funded by a card-linked account.
	switch {
	case reATM.MatchString(text):
		return model.ChannelATM
	case reAutoPay.MatchString(text):
		return model.ChannelAutoPay
	case reIMPS.MatchString(text):
		return model.ChannelIMPS
	case reNEFT.MatchString(text):
		return model.ChannelNEFT
	case reUPI.MatchString(text):
		return model.ChannelUPI
	case reWallet.MatchString(text):
		return model.ChannelWallet
	case reCard.MatchString(text):
		return model.ChannelCard
	}
	return model.ChannelUnknown
}

// ---------------------------------------------------------------------------
// Merchant
// ---------------------------------------------------------------------------

// merchantStop is the set of boundaries that end a counterparty name.
//
// Punctuation is the easy half. The hard half is that payment-app pushes run
// clauses together with no punctuation at all, so the next verb or preposition
// has to act as the terminator instead.
const merchantStop = `\s+on\b|\s+ref\b|\s+upi\b|\s+txn\b|\s+transaction\b|` +
	`\s+debited\b|\s+credited\b|\s+from\b|\s+using\b|\s+via\b|\s+dated\b|[.;,(]|$`

var merchantPatterns = []*regexp.Regexp{
	// Explicit VPA is the most reliable counterparty signal.
	regexp.MustCompile(`(?i)\bvpa\s+([a-z0-9._-]+@[a-z]+)`),
	regexp.MustCompile(`(?i)\b(?:to|from)\s+([a-z0-9._-]+@[a-z]{2,})\b`),
	// SBI: "trf to SWIGGY Refno 5223..."
	regexp.MustCompile(`(?i)\btrf\s+to\s+(.+?)(?:\s+ref\s*no|\s+refno|\s+ref\b|\.|$)`),
	// HDFC: "To SWIGGY On 12/08/25". An opening bracket terminates the match
	// because issuers append "(UPI Ref no ...)" directly after the name, and so
	// does a following verb: payment-app pushes run clauses together without
	// punctuation ("to Croma Electronics Debited from HDFC Bank ...").
	regexp.MustCompile(`(?i)\b(?:to|towards)\s+(?:vpa\s+)?(.+?)(?:` + merchantStop + `)`),
	// Card POS: "at BIGBASKET on 12-08" / "at UBER INDIA on"
	regexp.MustCompile(`(?i)\bat\s+(.+?)(?:\s+avl\b|` + merchantStop + `)`),
	// Credits: "credited ... from SALARY"
	regexp.MustCompile(`(?i)\bfrom\s+(.+?)(?:\s+on\b|\s+ref\b|[.;,(]|$)`),
	// Reversals and inbound transfers: "credited to your Card xx5678 by AMAZON"
	regexp.MustCompile(`(?i)\bby\s+([a-z][a-z0-9 &._*-]{2,}?)(?:\s+on\b|\s+ref\b|[.;,(]|$)`),
	// ICICI: "; AMAZON credited." The counterparty must start with a letter,
	// otherwise the comma inside "Rs.1,299.00" makes the amount look like a
	// clause boundary and the digits get captured as a merchant name.
	regexp.MustCompile(`(?i)[;,]\s*([a-z][a-z0-9 &._*-]{2,}?)\s+credited\b`),
	// Axis card: "12-08-25 12:30:45 BIGBASKET Avl Lmt"
	regexp.MustCompile(`(?i)\d{2}:\d{2}:\d{2}\s+(.+?)(?:\s+avl\b|[.;,]|$)`),
	// "Info: UPI/SWIGGY/5223" style narration.
	regexp.MustCompile(`(?i)\binfo[:\s]+(?:upi[/-])?([a-z0-9 &._*-]{3,}?)(?:[/.;,]|$)`),
}

// Tokens that indicate the regex swallowed boilerplate instead of a merchant.
var merchantReject = regexp.MustCompile(`(?i)^(your|the|a/?c|acct|account|card|bank|customer|dear|call|sms|block|avl|available|balance|upi|txn|transaction|date|ref|not\s+you|link|click|www|http)\b`)

func extractMerchant(text string) string {
	for _, re := range merchantPatterns {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		cand := cleanMerchant(m[1])
		if cand == "" || len(cand) < 2 || len(cand) > 64 {
			continue
		}
		if merchantReject.MatchString(cand) {
			continue
		}
		// A counterparty name that opens with a digit is almost always a
		// fragment of an amount or a reference number that the pattern
		// over-captured.
		if cand[0] >= '0' && cand[0] <= '9' {
			continue
		}
		// A candidate that is only digits is a reference number we mis-grabbed.
		if isMostlyDigits(strings.ReplaceAll(cand, " ", "")) {
			continue
		}
		return cand
	}
	return ""
}

var (
	trailingNoise = regexp.MustCompile(`(?i)[\s.,;:-]+$`)
	// A bare "VPA" prefix survives when the handle has no @ — some issuers
	// write "to VPA SOME MERCHANT" for non-UPI counterparties.
	leadingVPA = regexp.MustCompile(`(?i)^(vpa|upi|to|at)\s+`)
)

func cleanMerchant(s string) string {
	s = strings.TrimSpace(s)
	// Anything from an opening bracket onwards is issuer boilerplate:
	// "(UPI Ref no 5223...)", "(IMPS Ref ...)".
	if i := strings.IndexByte(s, '('); i > 0 {
		s = s[:i]
	}
	s = leadingVPA.ReplaceAllString(strings.TrimSpace(s), "")
	s = trailingNoise.ReplaceAllString(s, "")
	// Narration fields are slash-delimited: "UPI/SWIGGY/522345" -> "SWIGGY".
	if parts := strings.Split(s, "/"); len(parts) > 1 {
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || isMostlyDigits(p) || strings.EqualFold(p, "upi") {
				continue
			}
			s = p
			break
		}
	}
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// Account hint and reference
// ---------------------------------------------------------------------------

var reAccount = regexp.MustCompile(`(?i)\b(?:a/?c|acct|account|card)(?:\s*(?:no|number))?\s*\.?\s*:?\s*((?:x+|\*+)\s*\d{3,6}|\d{4,6})\b`)

// extractAccount returns a masked account tail in the canonical form "XX4412".
// Only the last four digits are ever retained; khata has no use for a full
// account number and refuses to store one.
func extractAccount(text string) string {
	m := reAccount.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	digits := regexp.MustCompile(`\d+`).FindString(m[1])
	if len(digits) < 3 {
		return ""
	}
	if len(digits) > 4 {
		digits = digits[len(digits)-4:]
	}
	return "XX" + digits
}

var reReference = regexp.MustCompile(`(?i)\b(?:upi\s*ref(?:erence)?(?:\s*no)?|ref(?:erence)?\s*no|refno|ref|txn\s*id|transaction\s*id|utr|rrn)\s*\.?\s*:?\s*#?\s*([A-Za-z0-9]{6,25})\b`)

// ICICI compresses it to "UPI:522345678901".
var reReferenceCompact = regexp.MustCompile(`(?i)\bupi\s*:\s*([0-9]{9,18})\b`)

func extractReference(text string) string {
	if m := reReference.FindStringSubmatch(text); m != nil {
		return strings.ToUpper(m[1])
	}
	if m := reReferenceCompact.FindStringSubmatch(text); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// ---------------------------------------------------------------------------
// Timestamp
// ---------------------------------------------------------------------------

var dateLayouts = []string{
	"2006-01-02:15:04:05",
	"2006-01-02 15:04:05",
	"02-01-2006 15:04:05",
	"02/01/2006 15:04:05",
	"02-01-06 15:04:05",
	"02-Jan-2006 15:04:05",
	"02-01-2006",
	"02/01/2006",
	"02-01-06",
	"02/01/06",
	"02-Jan-2006",
	"02-Jan-06",
	"02Jan06",
	"02Jan2006",
	"2006-01-02",
}

var reDateCandidate = regexp.MustCompile(`(?i)\b(\d{4}-\d{2}-\d{2}[:\s]\d{2}:\d{2}:\d{2}|\d{2}[-/]\d{2}[-/]\d{2,4}\s+\d{2}:\d{2}:\d{2}|\d{2}-[a-z]{3}-\d{2,4}|\d{2}[a-z]{3}\d{2,4}|\d{2}[-/]\d{2}[-/]\d{2,4}|\d{4}-\d{2}-\d{2})\b`)

// extractTime pulls the transaction date out of the message when present.
//
// The device receive time is a poor substitute: bank SMS routinely arrive
// minutes to hours late, and a delayed message landing after midnight would
// otherwise be booked into the wrong day — and therefore the wrong month's
// budget.
func extractTime(text string, fallback time.Time) (time.Time, bool) {
	loc := fallback.Location()
	if loc == nil {
		loc = time.Local
	}
	for _, raw := range reDateCandidate.FindAllString(text, -1) {
		s := strings.ReplaceAll(raw, "/", "-")
		s = titleMonth(s)
		for _, layout := range dateLayouts {
			if t, err := time.ParseInLocation(layout, s, loc); err == nil {
				if t.Year() < 100 {
					t = t.AddDate(2000, 0, 0)
				}
				// Reject nonsense far outside the plausible window.
				if t.Year() < 2000 || t.After(fallback.AddDate(0, 0, 2)) {
					continue
				}
				// A date-only match keeps the message's clock time so
				// intra-day ordering stays sensible.
				if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
					t = time.Date(t.Year(), t.Month(), t.Day(),
						fallback.Hour(), fallback.Minute(), fallback.Second(), 0, loc)
				}
				return t, true
			}
		}
	}
	return fallback, false
}

// titleMonth fixes the case of three-letter month names so time.Parse accepts
// "12-AUG-25" and "12aug25" alike.
func titleMonth(s string) string {
	re := regexp.MustCompile(`(?i)[a-z]{3}`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ToUpper(m[:1]) + strings.ToLower(m[1:])
	})
}
