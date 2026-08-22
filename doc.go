// Package khata is the module root for the khata local-first expense ledger.
//
// The Go code in this module is the on-device engine: it parses bank
// notifications, categorises them against deterministic rule packs, stores
// them in a local append-only ledger, and evaluates monthly budgets. It has no
// network client, and noegress_test.go fails the build if one is ever added.
//
// See core/ for the engine, cmd/khata for the desktop CLI, android/ for the
// Android app, and toolchain/ for the offline Python tooling that compiles
// user corrections into rule packs.
package khata
