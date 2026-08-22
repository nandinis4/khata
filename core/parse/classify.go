package parse

import "regexp"

// Kind is the coarse intent of an incoming message. Getting this right matters
// more than any other single step: a budgeting app that books OTP amounts or
// balance enquiries as spending is worse than useless, and these messages
// outnumber real transactions in a typical inbox.
type Kind string

const (
	KindTransaction   Kind = "transaction"
	KindOTP           Kind = "otp"
	KindBalance       Kind = "balance"
	KindDeclined      Kind = "declined"
	KindMandateNotice Kind = "mandate_notice"
	KindCollectReq    Kind = "collect_request"
	KindPromotional   Kind = "promotional"
	KindUnrelated     Kind = "unrelated"
)

var (
	// An OTP message pairs the word with a short standalone code. Requiring
	// both avoids rejecting genuine debit alerts that merely carry a
	// "never share your OTP" footer.
	reOTPWord = regexp.MustCompile(`(?i)\b(otp|one[\s-]?time\s?(password|passcode|pin))\b`)
	reOTPCode = regexp.MustCompile(`(?i)(\b\d{4,8}\b\s+is\s+(the\s+)?(otp|one)|\b(otp|password|code|pin)\b[^.]{0,24}?\bis\b[\s:]*\d{4,8}\b|\bis\s+\d{4,8}\b[^.]{0,20}\botp\b|\b(otp|passcode|code)\b[\s:.\-]*\d{4,8}\b)`)

	reDebitVerb = regexp.MustCompile(`(?i)\b(debited|debit|spent|paid|withdrawn|withdrawal|purchase[sd]?|deducted|transferred\s+to|trf\s+to|sent)\b`)
	reCredVerb  = regexp.MustCompile(`(?i)\b(credited|credit|received|refund(ed)?|deposited|added\s+to)\b`)

	// Future-tense mandates announce a debit that has not happened yet.
	reFuture  = regexp.MustCompile(`(?i)\b(will\s+be\s+(debited|deducted|charged)|is\s+due|due\s+on|scheduled\s+(for|on)|upcoming|reminder)\b`)
	reMandate = regexp.MustCompile(`(?i)\b(mandate|autopay|standing\s+instruction|e-?nach|si\s+for)\b`)

	reDeclined = regexp.MustCompile(`(?i)\b(declined|failed|unsuccessful|could\s+not\s+be\s+(processed|completed)|has\s+been\s+rejected|insufficient\s+(funds|balance))\b`)

	reCollect = regexp.MustCompile(`(?i)\b(has\s+requested|collect\s+request|is\s+requesting|requested\s+money|payment\s+request)\b`)

	reBalanceOnly = regexp.MustCompile(`(?i)\b(avl\s*bal|available\s+balance|closing\s+balance|bal(ance)?\s+(is|as\s+on)|a/?c\s+balance)\b`)

	rePromo = regexp.MustCompile(`(?i)\b(pre-?approved|apply\s+now|click\s+(here|the)|offer\s+valid|limited\s+period|lowest\s+interest|personal\s+loan|credit\s+card\s+offer|download\s+the\s+app|t&c\s+apply|congratulations|win\s+|voucher\s+code|unsubscribe)\b`)

	// A reversal is a credit back to the account, not a failure.
	reReversal = regexp.MustCompile(`(?i)\b(reversed|reversal|refunded\s+to)\b`)
)

// Classify decides what a message is before any field extraction runs.
//
// Order is significant. OTP and declined checks come first because those
// messages contain amounts and transaction verbs and would otherwise sail
// through. Reversals are rescued ahead of the declined check because
// "transaction reversed" is a real credit.
func Classify(text string) Kind {
	switch {
	case reOTPWord.MatchString(text) && reOTPCode.MatchString(text):
		return KindOTP

	case reReversal.MatchString(text) && reCredVerb.MatchString(text):
		return KindTransaction

	case reDeclined.MatchString(text):
		return KindDeclined

	case reCollect.MatchString(text):
		return KindCollectReq

	case reFuture.MatchString(text) && !reReversal.MatchString(text):
		// Future tense wins even without the mandate keyword: "Rs 199 will be
		// debited on 15-08" is a notice, not a debit.
		return KindMandateNotice

	case reMandate.MatchString(text) && !reDebitVerb.MatchString(text):
		return KindMandateNotice

	case reDebitVerb.MatchString(text) || reCredVerb.MatchString(text):
		return KindTransaction

	case reBalanceOnly.MatchString(text):
		return KindBalance

	case rePromo.MatchString(text):
		return KindPromotional
	}
	return KindUnrelated
}

// Bookable reports whether a message kind should produce a ledger entry.
func (k Kind) Bookable() bool { return k == KindTransaction }
