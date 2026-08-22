// Command khata is the desktop companion to the Android app.
//
// It drives the same engine the phone runs, which makes it the fastest way to
// try the project without building an APK: pipe a few bank messages at
// `khata parse` and watch what the parser makes of them.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nandinisharma3120/khata/core/budget"
	"github.com/nandinisharma3120/khata/core/engine"
	"github.com/nandinisharma3120/khata/core/model"
	"github.com/nandinisharma3120/khata/core/parse"
	"github.com/nandinisharma3120/khata/core/rules"
)

const usage = `khata — a local-first expense ledger that reads bank notifications
             and never sends them anywhere.

usage: khata <command> [flags]

  parse   <text>              parse one message and print the result, without storing it
          -json               emit that result as JSON
          -batch              read messages from stdin, one per line, emit JSON lines
  import  <file>              import messages, one per line, or a JSON array of messages
  month   [YYYY-MM]           show the ledger and budget status for a month
  review                      list transactions awaiting a category decision
  set     <txn-id> <category> assign a category and record it as a correction
  budget  <category> <amount> set a monthly limit, in rupees
  rules                       print the active rule pack
  export                      write the correction log to stdout as JSON
  compact                     rewrite the ledger, dropping superseded records

global flags:
  -ledger <path>   ledger file (default: $XDG_DATA_HOME/khata/ledger.jsonl)
  -pack   <path>   extra rule pack layered over the builtin one

Nothing in this program opens a network connection. See noegress_test.go.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "khata:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("khata", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ledgerPath := fs.String("ledger", defaultLedgerPath(), "path to the ledger file")
	packPath := fs.String("pack", "", "additional rule pack to load")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("no command given")
	}

	cmd, cmdArgs := rest[0], rest[1:]

	// `parse` and `rules` are read-only and deliberately do not touch the
	// ledger, so they work before anything is set up.
	switch cmd {
	case "parse":
		return cmdParse(cmdArgs)
	case "rules":
		return cmdRules()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}

	var extra []string
	if *packPath != "" {
		extra = append(extra, *packPath)
	}
	eng, err := engine.New(engine.Options{
		LedgerPath:     *ledgerPath,
		ExtraPackPaths: extra,
		Location:       time.Local,
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	switch cmd {
	case "import":
		return cmdImport(eng, cmdArgs)
	case "month":
		return cmdMonth(eng, cmdArgs)
	case "review":
		return cmdReview(eng)
	case "set":
		return cmdSet(eng, cmdArgs)
	case "budget":
		return cmdBudget(eng, cmdArgs)
	case "export":
		return cmdExport(eng)
	case "compact":
		return cmdCompact(eng, *ledgerPath)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// defaultLedgerPath follows the XDG base directory spec so the ledger lands
// somewhere predictable and backup tools can find it.
func defaultLedgerPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "khata.jsonl"
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "khata", "ledger.jsonl")
}

// cmdParse shows exactly what the engine extracts from one message. It stores
// nothing, which makes it safe to paste a real SMS into.
//
// The -batch mode exists so the Python eval harness can measure this exact
// parser rather than a reimplementation of it: one message per line in, one
// JSON result per line out, one subprocess for the whole corpus.
func cmdParse(args []string) error {
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	batch := fs.Bool("batch", false, "read one message per line from stdin, write one JSON result per line")
	asJSON := fs.Bool("json", false, "emit the result as JSON instead of a human-readable summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *batch {
		return parseBatch(os.Stdin, os.Stdout)
	}

	text := strings.Join(fs.Args(), " ")
	if text == "" {
		b, err := readAll(os.Stdin)
		if err != nil {
			return err
		}
		text = strings.TrimSpace(b)
	}
	if text == "" {
		return errors.New("nothing to parse; pass text as an argument or on stdin")
	}

	res := parse.Parse(model.Message{SourcePackage: "cli", Body: text, ReceivedAt: time.Now()})

	if *asJSON {
		return emitJSON(os.Stdout, text, res)
	}
	if !res.Bookable {
		fmt.Printf("not booked  (%s)\n  reason: %s\n", res.Kind, res.Reason)
		return nil
	}

	eng, err := rules.NewDefaultEngine()
	if err != nil {
		return err
	}
	d := eng.Categorize(*res.Txn)

	t := res.Txn
	fmt.Printf("booked\n")
	fmt.Printf("  amount      ₹%s (%s)\n", budget.Money(t.AmountMinor), t.Direction)
	fmt.Printf("  merchant    %s\n", orDash(t.MerchantRaw))
	fmt.Printf("  key         %s\n", orDash(t.MerchantKey))
	fmt.Printf("  channel     %s\n", t.Channel)
	fmt.Printf("  account     %s\n", orDash(t.AccountHint))
	fmt.Printf("  reference   %s\n", orDash(t.Reference))
	fmt.Printf("  occurred    %s\n", t.OccurredAt.Format("2006-01-02 15:04"))
	fmt.Printf("  category    %s", d.Category)
	if d.Matched {
		fmt.Printf("  (rule %s, %s)", d.RuleID, d.Source)
	}
	fmt.Println()
	fmt.Printf("  confidence  %.2f", t.Confidence)
	if t.Confidence < model.ReviewThreshold {
		fmt.Printf("  — below the %.2f review threshold, would be queued for confirmation", model.ReviewThreshold)
	}
	fmt.Println()
	return nil
}

// batchResult is the machine-readable shape emitted by `khata parse -batch`.
// It is the eval harness's contract with the engine, so the field names are
// stable and every field the harness scores on is present even when empty.
type batchResult struct {
	Input       string  `json:"input"`
	Bookable    bool    `json:"bookable"`
	Kind        string  `json:"kind"`
	Reason      string  `json:"reason,omitempty"`
	AmountMinor int64   `json:"amount_minor"`
	Direction   string  `json:"direction"`
	Channel     string  `json:"channel"`
	MerchantKey string  `json:"merchant_key"`
	AccountHint string  `json:"account_hint"`
	Reference   string  `json:"reference"`
	Category    string  `json:"category"`
	RuleID      string  `json:"rule_id"`
	Confidence  float64 `json:"confidence"`
	NeedsReview bool    `json:"needs_review"`
	OccurredAt  string  `json:"occurred_at"`
}

func parseBatch(in io.Reader, out io.Writer) error {
	ruleEng, err := rules.NewDefaultEngine()
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	enc := json.NewEncoder(out)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := enc.Encode(buildResult(ruleEng, line)); err != nil {
			return err
		}
	}
	return sc.Err()
}

func emitJSON(out io.Writer, text string, _ parse.Result) error {
	ruleEng, err := rules.NewDefaultEngine()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(buildResult(ruleEng, text))
}

func buildResult(ruleEng *rules.Engine, text string) batchResult {
	res := parse.Parse(model.Message{SourcePackage: "cli", Body: text, ReceivedAt: time.Now()})
	out := batchResult{Input: text, Kind: string(res.Kind), Bookable: res.Bookable, Reason: res.Reason}
	if !res.Bookable {
		return out
	}
	t := res.Txn
	d := ruleEng.Categorize(*t)
	out.AmountMinor = t.AmountMinor
	out.Direction = string(t.Direction)
	out.Channel = string(t.Channel)
	out.MerchantKey = t.MerchantKey
	out.AccountHint = t.AccountHint
	out.Reference = t.Reference
	out.Category = d.Category
	out.RuleID = d.RuleID
	out.Confidence = t.Confidence
	out.NeedsReview = t.NeedsReview
	out.OccurredAt = t.OccurredAt.Format("2006-01-02")
	return out
}

// cmdImport accepts either a plain-text file with one message per line, which
// is what most SMS backup tools can produce, or a JSON array of Message
// objects for anything richer.
func cmdImport(eng *engine.Engine, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: khata import <file>")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()

	var msgs []model.Message
	raw, err := readAll(f)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(raw)

	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &msgs); err != nil {
			return fmt.Errorf("parse JSON import: %w", err)
		}
	} else {
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			msgs = append(msgs, model.Message{SourcePackage: "import", Body: line, ReceivedAt: time.Now()})
		}
	}

	var booked, dupes, skipped int
	reasons := map[string]int{}
	for _, m := range msgs {
		if m.ReceivedAt.IsZero() {
			m.ReceivedAt = time.Now()
		}
		res, err := eng.Ingest(m)
		if err != nil {
			return err
		}
		switch {
		case res.Booked:
			booked++
		case res.Duplicate:
			dupes++
		default:
			skipped++
			reasons[res.Reason]++
		}
	}

	fmt.Printf("%d message(s): %d booked, %d duplicate, %d skipped\n", len(msgs), booked, dupes, skipped)
	if len(reasons) > 0 {
		fmt.Println("\nskipped because:")
		for _, r := range sortedKeys(reasons) {
			fmt.Printf("  %3d  %s\n", reasons[r], r)
		}
	}
	return nil
}

func cmdMonth(eng *engine.Engine, args []string) error {
	at := time.Now()
	if len(args) == 1 {
		parsed, err := time.ParseInLocation("2006-01", args[0], time.Local)
		if err != nil {
			return fmt.Errorf("month must look like 2025-08: %w", err)
		}
		at = parsed
	}

	txns, err := eng.Month(at.Year(), at.Month())
	if err != nil {
		return err
	}
	states, err := eng.MonthStates(at)
	if err != nil {
		return err
	}

	fmt.Printf("%s — %d transaction(s)\n\n", at.Format("January 2006"), len(txns))

	if len(states) > 0 {
		fmt.Printf("%-20s %12s %12s %7s  %s\n", "CATEGORY", "SPENT", "LIMIT", "USED", "STATUS")
		for _, s := range states {
			limit, pct := "—", "—"
			if s.LimitMinor > 0 {
				limit = "₹" + budget.Money(s.LimitMinor)
				pct = fmt.Sprintf("%.0f%%", s.Pct)
			}
			fmt.Printf("%-20s %12s %12s %7s  %s\n",
				s.Category, "₹"+budget.Money(s.SpentMinor), limit, pct, statusLabel(s.Status))
		}
		fmt.Println()
	}

	if len(txns) > 0 {
		fmt.Printf("%-26s %-11s %-18s %s\n", "WHEN", "AMOUNT", "CATEGORY", "MERCHANT")
		for _, t := range txns {
			sign := "-"
			if t.Direction == model.DirectionCredit {
				sign = "+"
			}
			flag := ""
			if t.NeedsReview {
				flag = "  ?"
			}
			fmt.Printf("%-26s %-11s %-18s %s%s\n",
				t.OccurredAt.Format("2006-01-02 15:04")+" "+shortID(t.ID),
				sign+"₹"+budget.Money(t.AmountMinor),
				t.Category, orDash(t.MerchantRaw), flag)
		}
	}
	return nil
}

func cmdReview(eng *engine.Engine) error {
	txns, err := eng.NeedsReview(50)
	if err != nil {
		return err
	}
	if len(txns) == 0 {
		fmt.Println("nothing to review")
		return nil
	}
	fmt.Printf("%d transaction(s) need a decision:\n\n", len(txns))
	for _, t := range txns {
		fmt.Printf("  %s  ₹%-10s %-24s conf %.2f\n",
			shortID(t.ID), budget.Money(t.AmountMinor), orDash(t.MerchantRaw), t.Confidence)
		fmt.Printf("      %s\n", truncate(t.RawText, 96))
	}
	fmt.Printf("\nassign one with:  khata set <id> <category>\n")
	fmt.Printf("available:        %s\n", strings.Join(eng.Categories(), ", "))
	return nil
}

func cmdSet(eng *engine.Engine, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: khata set <txn-id> <category>")
	}
	id, category := args[0], args[1]

	// Accept the short id the listing prints, not just the full one.
	if len(id) < 24 {
		full, err := resolveShortID(eng, id)
		if err != nil {
			return err
		}
		id = full
	}
	if err := eng.Correct(id, category); err != nil {
		return err
	}
	fmt.Printf("set %s to %s, recorded as a correction\n", shortID(id), category)
	fmt.Println("run the toolchain's rule synthesis to turn corrections like this into rules")
	return nil
}

// resolveShortID finds the one transaction whose id starts with the prefix.
func resolveShortID(eng *engine.Engine, prefix string) (string, error) {
	if prefix == "" {
		return "", errors.New("no transaction id given")
	}
	now := time.Now()
	var matches []string
	for i := 0; i < 14; i++ { // search back a year, plus the current month
		m := now.AddDate(0, -i, 0)
		txns, err := eng.Month(m.Year(), m.Month())
		if err != nil {
			return "", err
		}
		for _, t := range txns {
			if strings.HasPrefix(t.ID, prefix) {
				matches = append(matches, t.ID)
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no transaction id starts with %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q is ambiguous, it matches %d transactions", prefix, len(matches))
	}
}

func cmdBudget(eng *engine.Engine, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: khata budget <category> <rupees>")
	}
	rupees, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return fmt.Errorf("amount must be a number: %w", err)
	}
	minor := int64(rupees*100 + 0.5)
	if err := eng.SetEnvelope(args[0], minor); err != nil {
		return err
	}
	fmt.Printf("%s budget set to ₹%s a month\n", args[0], budget.Money(minor))
	return nil
}

func cmdExport(eng *engine.Engine) error {
	corr, err := eng.Corrections()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(corr); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%d correction(s). This file carries merchant names and categories only:\n", len(corr))
	fmt.Fprintln(os.Stderr, "no amounts, no dates, no account numbers, no message text.")
	return nil
}

func cmdRules() error {
	os.Stdout.Write(rules.BuiltinPackJSON())
	return nil
}

func cmdCompact(eng *engine.Engine, path string) error {
	before := fileSize(path)
	if err := eng.Compact(); err != nil {
		return err
	}
	after := fileSize(path)
	fmt.Printf("compacted %s: %d -> %d bytes\n", path, before, after)
	return nil
}

// fileSize reports a file's size, or 0 if it cannot be read. Compaction
// reporting is cosmetic, so a stat failure should not fail the command.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// --- small helpers ---------------------------------------------------------

func statusLabel(s string) string {
	switch s {
	case budget.StatusOver:
		return "OVER"
	case budget.StatusWarn:
		return "warn"
	case budget.StatusProjectedOver:
		return "on pace to overspend"
	}
	return "ok"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
}
