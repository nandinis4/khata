# khata

**A local-first expense ledger that reads your bank notifications and cannot send them anywhere.**

Every UPI debit, card swipe and ATM withdrawal already arrives on your phone as a
notification. The data is right there. Every app that mines it ships it to a
server first, because the business model is the data.

khata does the same job on the device and takes the network away from itself, so
that promise is not something you have to believe.

```
$ khata parse "Rs.450.00 debited from a/c XXXXXX4412 on 12-08-25 to VPA swiggy@ybl (UPI Ref no 522345678901)"
booked
  amount      ₹450.00 (debit)
  merchant    swiggy@ybl
  key         swiggy
  channel     upi
  account     XX4412
  reference   522345678901
  occurred    2025-08-12 17:26
  category    food_delivery  (rule food.swiggy, builtin)
  confidence  1.00
```

```
$ khata month 2025-08

CATEGORY                    SPENT        LIMIT    USED  STATUS
groceries                  ₹3,860       ₹4,000     96%  warn
transport                  ₹1,209       ₹2,000     60%  ok
food_delivery                ₹150       ₹1,500     10%  ok
rent                      ₹18,000            —       —  ok
```

---

## The privacy guarantee, and why you don't have to trust it

Three independent mechanisms, each verifiable in under a minute:

**1. The Android app has no `INTERNET` permission.** Not "does not use the
network" — cannot. The runtime refuses every socket the process opens. Check it
yourself:

```sh
aapt dump permissions khata.apk | grep INTERNET   # returns nothing
```

CI asserts this on the built APK on every push, so it cannot regress quietly.

**2. A test fails the build if a network import appears in the engine.**
[`noegress_test.go`](noegress_test.go) parses every non-test Go file that ships
on-device and rejects `net`, `net/http`, `os/exec`, `crypto/tls` and friends. Add
an analytics SDK and CI tells you, in a failure message that explains why the
rule exists.

**3. The engine has zero third-party dependencies.** `go.sum` does not exist.
The entire code path that touches your bank messages is the Go standard library
and this repository — which is what makes points 1 and 2 checkable at all, since
a dependency could open a socket without this module ever naming a `net` package.

Backup is off too: `allowBackup="false"` plus explicit extraction rules, because
Android's default cloud backup would copy your ledger to Google Drive as a side
effect of a system feature you never associated with this app.

## Where the AI is — and deliberately isn't

The interesting design decision in this project is that **no model runs at
transaction time.**

A model in the hot path means one of two things: shipping merchant strings to a
provider, which is the thing this project exists to avoid, or bundling a local
model large enough to hurt install size and battery for a task that is mostly
string matching.

So the model runs offline, on your laptop, and its *output is a rule file*:

```
  you correct a category  ──▶  correction log  ──▶  khata-tools synth  ──▶  rule pack
        (on the phone)          (merchant + category,      (LLM proposes         (plain JSON,
                                 no amounts, no dates)      a match expression)   ships to phone)
                                                                  │
                                                                  ▼
                                                        validated against your
                                                        own corrections before
                                                        it is allowed to ship
```

The model never sees a transaction. It sees a category and the merchant keys you
filed under it, and it proposes a *match expression*. That proposal is then
checked against your actual corrections and thrown away unless it:

- matches **every** merchant you put in that category, and
- matches **none** of the merchants you put in a different one.

A confident wrong answer fails exactly like a hesitant wrong answer. When a
proposal fails, synthesis falls back to the literal merchant names, which are
always safe. What ships is JSON you can read in a diff, identical on every
device, fast enough to run inside a notification callback.

```
$ khata-tools synth corrections.json
rule synthesis (heuristic)
====================================================
rules produced   2
proposals rejected 0

rules
  emergency_fund     <- atm
  office_supplies    <- qzxw traders
```

**The LLM is a compiler, not an oracle.** That framing is the point: it makes
the model's contribution auditable, cacheable, and free at runtime, and it means
a bad generation degrades coverage instead of corrupting your budget.

## The interaction

A transaction the engine can file confidently is filed silently. One it cannot
becomes a single notification with the likely answers as buttons — tap one and
it is filed, and that answer becomes a rule so the same merchant never asks
again.

Restraint is a feature here. An app that notifies on every transaction gets
muted within a week, and a muted app captures nothing. Budget alerts fire **once
per threshold per category per month**, durably recorded, so a crossing cannot
re-fire on every subsequent purchase.

## How it works

```
┌─────────────────────────── Android (Kotlin, Compose) ────────────────────────┐
│  NotificationListenerService          SmsCapture                             │
│  (allowlisted bank & payment apps)    (institutional short codes only)       │
│                    │                        │                                │
│                    └────────────┬───────────┘                                │
│                                 ▼                                            │
│                      ┌────────────────────┐                                  │
│                      │  khata.aar         │  ← gomobile bind ./core/mobile   │
└──────────────────────┴────────┬───────────┴──────────────────────────────────┘
                                ▼
┌──────────────────────── Go engine (no third-party deps) ─────────────────────┐
│  parse    classify → reject OTP/balance/declined/mandate, then extract       │
│           amount · direction · channel · merchant · account · ref · date     │
│  rules    ordered deterministic match → category + the rule that decided     │
│  ledger   append-only JSONL, fsynced, two-tier dedup, event-sourced          │
│  budget   envelopes, straight-line projection, once-per-crossing alerts      │
└──────────────────────────────────────────────────────────────────────────────┘
                                ▲                        │
                    same engine │                        │ corrections
                                │                        ▼
┌────────────── Python toolchain (off-device, run by hand) ────────────────────┐
│  evals      50-case corpus → per-field accuracy, false/missed booking gates  │
│  rulesynth  corrections → LLM proposal → validation → rule pack              │
│  report     ledger → monthly markdown, recurring-charge detection            │
└──────────────────────────────────────────────────────────────────────────────┘
```

The Android app and the desktop CLI drive the **same** Go engine — the phone
links it through gomobile, the CLI links it directly. They cannot disagree about
how a message is parsed, because there is only one parser.

## The parser

The hard part is not extracting an amount. It is **not** extracting the wrong one.

A typical debit alert quotes both what you spent and what you have left. Naively
taking the first or largest match books your account balance as a purchase. So
every candidate amount is scored on its surrounding words — balance and limit
context pushes it down, transaction verbs pull it up — and the margin between the
winner and the runner-up feeds the parse's confidence score.

Before that, a classifier throws out the messages that would do the most damage:

| kind | example | why it must not be booked |
|---|---|---|
| `otp` | `123456 is the OTP for txn of Rs 2,500 at AMAZON` | contains an amount, isn't a payment |
| `balance` | `Avl Bal is Rs 18,110.42 as on 12-08-25` | nothing moved |
| `declined` | `Your txn of Rs 500 has been declined` | it didn't happen |
| `mandate_notice` | `Rs 199 for NETFLIX **will be** debited on 15-08` | hasn't happened yet |
| `collect_request` | `RAHUL **has requested** Rs 800` | no money moved |
| `promotional` | `Pre-approved loan up to Rs 5,00,000` | marketing |

Every one of these carries a rupee figure, and every one silently corrupts a
budget in a way you would not notice for months.

Other things the parser gets right because getting them wrong is expensive:

- **Amounts are integer paise.** Never a float. A budgeting app that accumulates
  rounding error is broken in a way that is very hard to see.
- **A debit at 23:50 on 31 August belongs to August**, even when the SMS lands at
  00:05 on 1 September. Booking by arrival time silently misstates two months.
- **The same payment arrives twice** — once from the bank, once from the payment
  app. Dedup is two-tier: an exact key on the shared bank reference, and a
  ±3-minute fuzzy probe on amount, direction and merchant for messages that
  carry no reference at all.
- **`bluetokai@icici` and `RAZ*BLUE TOKAI COFFEE BLR` are one merchant.**
  Normalisation strips acquirer prefixes, legal suffixes, city codes and terminal
  IDs; matching compares despaced forms so one rule covers both spellings.

## Evaluation

```
$ make eval
khata parser evaluation
====================================================
cases              50
fully correct      50  (100.0%)

false bookings     0   <- must be 0
missed bookings    0
```

The two gates are reported separately on purpose. **False bookings** — recording
a non-transaction as spending — are silent and corrupting, and the gate is zero.
**Missed bookings** cost coverage but are visible and recoverable. Averaging them
into one accuracy number would hide the one that matters.

**An honest caveat:** the corpus and the parser were written by the same person,
so 100% means "no known regressions", not "solved". Eight of the fifty cases were
written adversarially, against the parser rather than for it, and two of them
failed on first run and were fixed. Foreign-currency charges are a known open gap
and are in the corpus as documentation of it.

The harness drives the real Go binary through `khata parse -batch` rather than a
Python reimplementation, so the numbers describe the code that actually runs on
the phone. Adding a case is the preferred way to report a parser bug: write the
message and the result you expected, watch it fail, then fix it.

## Design decisions

**Why an append-only JSONL ledger instead of SQLite.** SQLite is the obvious
choice and for a multi-table app it would be right. This workload is different:
one person's transactions, a few thousand rows a year, always read in whole
months, never joined. An append-only log replayed into memory buys three things
that matter here — no third-party code in the path that touches financial data,
a small gomobile binary, and a format you can read with `cat` and take elsewhere
without trusting this program's export. The cost is real: everything lives in
memory and there is no query planner. At ~400 bytes a transaction, a decade of
heavy spending is under 30 MB. If the workload ever outgrows that, `Store` is an
interface.

**Why event-sourced.** A category correction is recorded as the correction it is,
not as an overwrite. That history is the training signal the toolchain compiles
into rules, and it means no user action is silently destructive. Compaction
collapses superseded records while preserving corrections.

**Why fsync on every append.** A budgeting app writes a handful of records a day.
Losing an acknowledged transaction to a battery pull is worse than a few
milliseconds. A torn trailing line from a power loss mid-write is detected and
repaired at open — the log is valid up to the last complete record, and the
interrupted one was never acknowledged.

**Why an allowlist of packages, not a blocklist.** A notification listener sees
*everything* on the device. An unknown app's notifications are none of khata's
business. A missing bank costs one pull request; a too-broad filter costs someone
their private messages.

## Getting started

### The CLI (no Android needed)

```sh
git clone https://github.com/nandinisharma3120/khata
cd khata
make demo
```

That imports [`testdata/sample_messages.txt`](testdata/sample_messages.txt) — 29
synthetic messages including the ones that must be rejected — and prints a month.

```sh
khata parse "<paste a bank SMS>"    # parses and prints, stores nothing
khata import backup.txt             # one message per line
khata review                        # what needs a decision
khata set <id> groceries            # file it; recorded as a correction
khata budget groceries 4000         # ₹4,000 a month
khata month 2025-08
khata export > corrections.json     # merchants and categories only
```

### The toolchain

```sh
pip install -e './toolchain[dev]'

khata-tools eval                              # measure the parser
khata-tools synth corrections.json -o pack.json   # compile corrections into rules
khata-tools report ~/.local/share/khata/ledger.jsonl
```

Rule synthesis defaults to the `heuristic` provider: no model, no network, no
configuration. `--provider ollama` uses a model on your own machine;
`--provider anthropic` uses a hosted one and needs `ANTHROPIC_API_KEY`. All three
go through the same validation gate.

### The Android app

```sh
go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init
make apk
```

Needs the Android NDK. CI builds this on every push and uploads the APK.

## Repository layout

```
core/parse      classification and field extraction
core/rules      deterministic rule packs and the matcher
core/ledger     append-only event log, dedup, compaction
core/budget     envelopes, projection, alert thresholds
core/engine     the object every front end drives
core/mobile     gomobile binding surface
cmd/khata       desktop CLI
android/        Kotlin + Compose app, notification capture
toolchain/      Python: evals, rule synthesis, reports
noegress_test.go  the test that enforces the privacy claim
```

## Status and limitations

Working: parsing, categorisation, the ledger, budgets and alerts, the correction
loop, rule synthesis, evaluation, reports, and the CLI — all tested. The Android
app compiles in CI and produces an APK; it has not been through a long soak on a
real device with real bank traffic.

Known gaps: only INR is handled, so a foreign-currency charge books the wrong
amount. The merchant allowlist covers major Indian banks and payment apps and
will miss smaller ones. Rule packs are per-device — there is no sync, by design.

## Contributing

The most useful contributions are a new bank's message shape added to
[`toolchain/evals/corpus.jsonl`](toolchain/evals/corpus.jsonl), a merchant added
to [`core/rules/packs/builtin.json`](core/rules/packs/builtin.json), or a bank
package added to the capture allowlist.

`make check` runs everything CI does apart from the Android build.

One rule: a pull request that adds the `INTERNET` permission, a network import,
or a third-party dependency to the engine is a change to what this project *is*,
not a feature. Open an issue first.

## License

MIT — see [LICENSE](LICENSE).

Every message in this repository is synthetic. No real transaction data appears
anywhere in it, and `.gitignore` is set up so a real ledger cannot be committed
by accident.
