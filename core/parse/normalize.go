package parse

import (
	"regexp"
	"strings"
)

// Merchant strings arriving from Indian banks and payment apps are a mess:
// they carry acquirer prefixes, city codes, terminal IDs, legal suffixes and
// arbitrary separators. Normalisation collapses that noise into a stable key
// so that "RAZ*BLUE TOKAI COFFEE BLR", "bluetokai@icici" and
// "Blue Tokai Coffee Roasters Pvt Ltd" all match one rule.

var (
	// Acquirer / aggregator prefixes, stripped from the head of the string.
	acquirerPrefix = regexp.MustCompile(`(?i)^(raz|razorpay|pytm|paytm|billdesk|ccavenue|payu|pine|cashfree|instamojo|juspay|bhim|upi|pos|nch|ach|ecom|imps|neft|mmt|atom|worldline|zomato pay)[\s*_\-/.]+`)

	// Legal-entity and geography suffixes that add no matching signal.
	noiseToken = map[string]bool{
		"pvt": true, "pvtltd": true, "ltd": true, "limited": true,
		"private": true, "llp": true, "inc": true, "co": true,
		"india": true, "ind": true, "in": true, "technologies": true,
		"technology": true, "services": true, "solutions": true,
		"enterprises": true, "retail": true, "online": true,
		"payments": true, "payment": true, "pay": true,
	}

	// Airport-style city codes that banks append to POS merchant names.
	cityCode = map[string]bool{
		"blr": true, "del": true, "bom": true, "maa": true, "hyd": true,
		"ccu": true, "pnq": true, "amd": true, "jai": true, "ggn": true,
		"noi": true, "ncr": true, "mum": true, "bng": true, "gurgaon": true,
		"bengaluru": true, "bangalore": true, "mumbai": true, "delhi": true,
		"chennai": true, "hyderabad": true, "pune": true, "kolkata": true,
	}

	// Terminal identifiers and reference tails: long digit runs, or tokens that
	// are mostly digits.
	longDigits  = regexp.MustCompile(`\d{4,}`)
	nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)
	spaceRun    = regexp.MustCompile(`\s+`)
)

// NormalizeMerchant reduces a raw counterparty string to a matchable key.
//
// The key is lowercase alphanumeric with single-space separators. It is
// intentionally lossy: it exists to be matched, not displayed. Keep
// MerchantRaw for anything a human reads.
func NormalizeMerchant(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// A VPA carries the useful handle before the '@'; the PSP suffix (ybl,
	// okaxis, paytm) identifies the payment app, not the merchant.
	if i := strings.Index(s, "@"); i > 0 && !strings.Contains(s, " ") {
		s = s[:i]
	}

	s = strings.ToLower(s)

	// Strip acquirer prefixes repeatedly: "raz*pytm*foo" happens.
	for {
		stripped := acquirerPrefix.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = stripped
	}

	// Separators become spaces so tokens can be filtered individually.
	s = nonAlphaNum.ReplaceAllString(s, " ")
	s = spaceRun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	fields := strings.Fields(s)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if noiseToken[f] || cityCode[f] {
			continue
		}
		// Drop pure digit runs and terminal-ID-looking tokens.
		if longDigits.MatchString(f) && isMostlyDigits(f) {
			continue
		}
		// Single characters are almost always separator debris.
		if len(f) == 1 && f != "5" {
			continue
		}
		kept = append(kept, f)
	}

	if len(kept) == 0 {
		// Everything was filtered; fall back to the de-punctuated form so we
		// never return an empty key for a non-empty input.
		return strings.TrimSpace(s)
	}
	return strings.Join(kept, " ")
}

func isMostlyDigits(s string) bool {
	d := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d++
		}
	}
	return d*2 >= len(s)
}

// TitleizeMerchant produces a display form from a raw merchant string. Banks
// SHOUT; this makes the UI readable without losing the original.
func TitleizeMerchant(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "@") && !strings.Contains(s, " ") {
		return s // VPAs are lowercase by convention; leave them alone.
	}
	// Only re-case strings that are entirely uppercase; mixed case was
	// probably deliberate.
	if s != strings.ToUpper(s) {
		return s
	}
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
