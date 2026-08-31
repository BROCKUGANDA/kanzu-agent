package rules

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/ledger"
)

// The engine is the compliance authority, so these tests pin every detector's
// behaviour against realistic Ugandan-SACCO transaction shapes (mirroring
// fixtures/transactions.csv: UGX minor-unit amounts, cash/mobile/agent/transfer
// channels, watchlisted-country counterparties). Each rule gets at least one
// firing case and one clean case. Everything runs against a throwaway SQLite
// ledger in t.TempDir(), so the suite is deterministic and isolated.

// Fixed window so windows and baselines never drift with wall time.
var (
	testStart = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	testEnd   = testStart.Add(7 * 24 * time.Hour)
)

func day(n int) time.Time { return testStart.Add(time.Duration(n) * 24 * time.Hour) }

// Default internal_report_threshold_minor: UGX 28,000,000 in minor units.
const ugx28M = int64(2_800_000_000)

type fixture struct {
	member ledger.Member
	txns   []ledger.Txn
}

func txn(id string, d, hour int, direction string, amountMinor int64) ledger.Txn {
	return ledger.Txn{
		ID: id, AccountID: "A-" + id, MemberID: "M-001",
		TS:        day(d).Add(time.Duration(hour) * time.Hour),
		Direction: direction, AmountMinor: amountMinor, Currency: "UGX",
	}
}

func withCountry(t ledger.Txn, country string) ledger.Txn {
	t.Country = country
	return t
}

func withChannel(t ledger.Txn, channel string) ledger.Txn {
	t.Channel = channel
	return t
}

// withAccount sets CounterpartyAccount on a transaction — the dedicated
// field introduced for the R09 multi-account cycling detector (F-03).
func withAccount(t ledger.Txn, account string) ledger.Txn {
	t.CounterpartyAccount = account
	return t
}

func setup(t *testing.T, f fixture) (*Engine, context.Context) {
	t.Helper()
	db, err := ledger.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	if err := db.UpsertMember(ctx, f.member); err != nil {
		t.Fatalf("upsert member: %v", err)
	}
	// Transactions FK-reference accounts(id), so seed one account and point
	// every fixture transaction at it.
	if err := db.UpsertAccount(ctx, "A-M-001", "M-001", "savings", "UGX", day(-400)); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	for i := range f.txns {
		f.txns[i].AccountID = "A-M-001"
	}
	for _, tx := range f.txns {
		if err := db.InsertTxn(ctx, tx); err != nil {
			t.Fatalf("insert %s: %v", tx.ID, err)
		}
	}
	eng, err := NewEngine(ctx, db)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng, ctx
}

// scanOne runs Scan restricted to one rule and keeps that rule's findings.
func scanOne(t *testing.T, eng *Engine, ctx context.Context, ruleID string) []Finding {
	t.Helper()
	out, err := eng.Scan(ctx, testStart, testEnd, []string{ruleID})
	if err != nil {
		t.Fatalf("scan %s: %v", ruleID, err)
	}
	var mine []Finding
	for _, f := range out {
		if f.RuleID == ruleID {
			mine = append(mine, f)
		}
	}
	return mine
}

func member(kyc int) ledger.Member {
	return ledger.Member{
		ID: "M-001", Name: "Nakato Sarah", JoinedAt: day(-400),
		KYCLevel: kyc, RiskBand: "medium", HomeBranch: "Kampala",
	}
}

func TestAllRulesPresent(t *testing.T) {
	want := []string{
		RuleStructuring, RuleVelocity, RuleRoundAmount, RuleDormantReact,
		RulePassThrough, RuleCrossBorder, RuleKYCGap, RuleThresholdHug,
		RuleMultiAcctCycle, RuleAgentConc,
	}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("All() = %d rules, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("All()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRuleDetectors(t *testing.T) {
	tests := []struct {
		name   string
		ruleID string
		fires  fixture // must produce >= 1 finding
		clean  fixture // must produce zero findings
		check  func(t *testing.T, f Finding)
	}{
		{
			name:   "R01 structuring",
			ruleID: RuleStructuring,
			// Five sub-threshold deposits totalling above UGX 28M inside 72h:
			// the classic placement schedule from the fixtures file.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				txn("T-01", 1, 9, "credit", 672_000_000),
				txn("T-02", 1, 14, "credit", 672_000_000),
				txn("T-03", 2, 10, "credit", 672_000_000),
				txn("T-04", 2, 16, "credit", 672_000_000),
				txn("T-05", 3, 11, "credit", 672_000_000),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Same shape but nowhere near the aggregate threshold.
				txn("T-06", 1, 9, "credit", 54_000_000),
				txn("T-07", 1, 14, "credit", 61_500_000),
				txn("T-08", 2, 10, "credit", 58_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Severity != SevHigh {
					t.Errorf("severity = %q, want high", f.Severity)
				}
				if f.Facts.Count < 3 {
					t.Errorf("count = %d, want >= 3", f.Facts.Count)
				}
				if f.Facts.TotalMinor < ugx28M {
					t.Errorf("total = %d, want >= threshold %d", f.Facts.TotalMinor, ugx28M)
				}
			},
		},
		{
			name:   "R02 velocity",
			ruleID: RuleVelocity,
			// more than 4x. Baseline() reads [start-168h, start), so history
			// sits in the days just before the scan window.
			fires: func() fixture {
				f := fixture{member: member(2)}
				for i := 1; i <= 4; i++ {
					f.txns = append(f.txns,
						txn(fmt.Sprintf("VB-%02d", i), -i, 6, "credit", 50_000_000))
				}
				f.txns = append(f.txns,
					txn("VT-01", 1, 10, "credit", 300_000_000),
					txn("VT-02", 3, 11, "credit", 300_000_000),
					txn("VT-03", 5, 12, "credit", 250_000_000),
				)
				return f
			}(),
			clean: func() fixture {
				f := fixture{member: member(2)}
				for i := 1; i <= 4; i++ {
					f.txns = append(f.txns,
						txn(fmt.Sprintf("CB-%02d", i), -i, 6, "credit", 50_000_000))
				}
				// Same trading rhythm as the baseline: no spike.
				f.txns = append(f.txns,
					txn("CT-01", 1, 10, "credit", 60_000_000),
					txn("CT-02", 3, 11, "credit", 55_000_000),
				)
				return f
			}(),
			check: func(t *testing.T, f Finding) {
				if f.Facts.Multiple < 4 {
					t.Errorf("multiple = %.2f, want >= 4", f.Facts.Multiple)
				}
			},
		},
		{
			name:   "R03 repeated round figures",
			ruleID: RuleRoundAmount,
			// UGX 6,720,000 repeats three times — divisible by the 1M-minor
			// round-figure modulus.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				txn("T-09", 1, 9, "credit", 672_000_000),
				txn("T-10", 2, 9, "credit", 672_000_000),
				txn("T-11", 3, 9, "credit", 672_000_000),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Genuine commerce produces untidy numbers.
				txn("T-12", 1, 9, "credit", 54_123_400),
				txn("T-13", 2, 11, "credit", 61_730_500),
				txn("T-14", 3, 12, "credit", 58_215_600),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.Count < 3 {
					t.Errorf("count = %d, want >= 3 repeats", f.Facts.Count)
				}
				if f.Facts.MaxMinor%1_000_000 != 0 {
					t.Errorf("max amount %d not round to modulus", f.Facts.MaxMinor)
				}
			},
		},
		{
			name:   "R04 dormant reactivation",
			ruleID: RuleDormantReact,
			// Last activity 200 days before the window, then material value.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				ledger.Txn{ID: "OLD-1", AccountID: "A-OLD", MemberID: "M-001",
					TS: day(-200), Direction: "credit", AmountMinor: 10_000_000, Currency: "UGX"},
				txn("T-15", 1, 10, "credit", 1_500_000_000),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Active a month ago: no dormancy gap.
				ledger.Txn{ID: "RECENT-1", AccountID: "A-R", MemberID: "M-001",
					TS: day(-30), Direction: "credit", AmountMinor: 10_000_000, Currency: "UGX"},
				txn("T-16", 1, 10, "credit", 1_500_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.DormantDays < 180 {
					t.Errorf("dormant days = %d, want >= 180", f.Facts.DormantDays)
				}
			},
		},
		{
			name:   "R05 rapid pass-through",
			ruleID: RulePassThrough,
			// Inbound UGX 25.2M, then UGX 21.84M leaves within hours (~87%):
			// the layering signature in fixtures/transactions.csv M-003.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				txn("T-17", 1, 10, "credit", 2_520_000_000),
				txn("T-18", 1, 15, "debit", 1_120_000_000),
				txn("T-19", 2, 9, "debit", 1_064_000_000),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Money arrives and stays: savings behaviour, not layering.
				txn("T-20", 1, 10, "credit", 2_520_000_000),
				txn("T-21", 3, 15, "debit", 120_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.Multiple < 80 {
					t.Errorf("outflow pct = %.1f, want >= 80", f.Facts.Multiple)
				}
			},
		},
		{
			name:   "R06 cross-border exposure",
			ruleID: RuleCrossBorder,
			// Two transfers from a watchlisted jurisdiction (SY appears in the
			// default high_risk_countries policy).
			fires: fixture{member: member(2), txns: []ledger.Txn{
				withCountry(txn("T-22", 2, 11, "credit", 980_000_000), "SY"),
				withCountry(txn("T-23", 5, 13, "credit", 840_000_000), "SY"),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Domestic flows carry no counterparty country.
				withCountry(txn("T-24", 2, 11, "credit", 980_000_000), "UG"),
				txn("T-25", 5, 13, "credit", 840_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if len(f.Facts.Countries) != 1 || f.Facts.Countries[0] != "SY" {
					t.Errorf("countries = %v, want [SY]", f.Facts.Countries)
				}
			},
		},
		{
			name:   "R07 KYC tier gap",
			ruleID: RuleKYCGap,
			// Unverified (tier 0) member transacting far beyond the UGX 5.6M
			// tier-1 ceiling.
			fires: fixture{member: member(0), txns: []ledger.Txn{
				txn("T-26", 2, 10, "credit", 980_000_000),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Fully verified member moving the same value: no gap.
				txn("T-27", 2, 10, "credit", 980_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Severity != SevHigh {
					t.Errorf("severity = %q, want high for tier-0 breach", f.Severity)
				}
				if f.Facts.KYCLevel != 0 {
					t.Errorf("kyc level = %d, want 0", f.Facts.KYCLevel)
				}
			},
		},
		{
			name:   "R08 threshold hugging",
			ruleID: RuleThresholdHug,
			// Two deposits parked between 85% and 100% of the UGX 28M line.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				txn("T-28", 1, 9, "credit", 2_450_000_000),  // UGX 24.5M
				txn("T-29", 3, 14, "credit", 2_660_000_000), // UGX 26.6M
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// One in-band deposit is a coincidence, not a pattern.
				txn("T-30", 1, 9, "credit", 2_450_000_000),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.Count != 2 {
					t.Errorf("count = %d, want 2", f.Facts.Count)
				}
				for _, id := range f.TxnIDs {
					if id != "T-28" && id != "T-29" {
						t.Errorf("unexpected txn %s in finding", id)
					}
				}
			},
		},
		{
			name:   "R09 multi-account cycling",
			ruleID: RuleMultiAcctCycle,
			// Funds credited across three sub-accounts (F-03: distinct
			// CounterpartyAccount values, NOT distinct Country codes) with
			// a large onward debit inside 48h. Outflow UGX 10M >= a quarter
			// of the 28M threshold.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				withAccount(txn("T-31", 1, 9, "credit", 1_200_000_000), "SUB-A"),
				withAccount(txn("T-32", 1, 10, "credit", 800_000_000), "SUB-B"),
				withAccount(txn("T-33", 2, 16, "debit", 1_000_000_000), "SUB-C"),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Only two destination accounts: ordinary transfers.
				withAccount(txn("T-34", 1, 9, "credit", 1_200_000_000), "SUB-A"),
				withAccount(txn("T-35", 2, 16, "debit", 1_000_000_000), "SUB-C"),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.Count < 3 {
					t.Errorf("accounts = %d, want >= 3", f.Facts.Count)
				}
				if f.Facts.WindowHours != 48 {
					t.Errorf("window = %dh, want 48", f.Facts.WindowHours)
				}
			},
		},
		{
			name:   "R10 agent concentration",
			ruleID: RuleAgentConc,
			// One MTN MoMo-style agent supplies almost all deposit value.
			fires: fixture{member: member(2), txns: []ledger.Txn{
				withChannel(txn("T-36", 1, 9, "credit", 336_000_000), "AGENT"),
				withChannel(txn("T-37", 3, 10, "credit", 336_000_000), "AGENT"),
				withChannel(txn("T-38", 4, 11, "credit", 336_000_000), "AGENT"),
				withChannel(txn("T-39", 6, 12, "credit", 336_000_000), "AGENT"),
				withChannel(txn("T-40", 6, 15, "credit", 100_000_000), "MOBILE"),
			}},
			clean: fixture{member: member(2), txns: []ledger.Txn{
				// Deposits spread evenly across channels: healthy mix.
				withChannel(txn("T-41", 1, 9, "credit", 336_000_000), "AGENT"),
				withChannel(txn("T-42", 3, 10, "credit", 336_000_000), "MOBILE"),
				withChannel(txn("T-43", 4, 11, "credit", 336_000_000), "CASH"),
				withChannel(txn("T-44", 6, 12, "credit", 336_000_000), "TRANSFER"),
			}},
			check: func(t *testing.T, f Finding) {
				if f.Facts.Multiple < 80 {
					t.Errorf("concentration = %.1f%%, want >= 80", f.Facts.Multiple)
				}
				if f.Facts.Count != 4 {
					t.Errorf("count = %d, want 4 agent deposits", f.Facts.Count)
				}
			},
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name+"/fires", func(t *testing.T) {
			eng, ctx := setup(t, tc.fires)
			found := scanOne(t, eng, ctx, tc.ruleID)
			if len(found) != 1 {
				t.Fatalf("%s: got %d findings, want exactly 1", tc.ruleID, len(found))
			}
			f := found[0]
			if f.MemberID != "M-001" {
				t.Errorf("member = %q, want M-001", f.MemberID)
			}
			if len(f.TxnIDs) == 0 {
				t.Errorf("%s fired without transaction evidence", tc.ruleID)
			}
			if f.Facts.Currency != "UGX" {
				t.Errorf("currency = %q, want UGX", f.Facts.Currency)
			}
			if tc.check != nil {
				tc.check(t, f)
			}
			// The deterministic description must render in every language
			// without panicking — this is what the narrator consumes.
			for _, lang := range []i18n.Lang{i18n.EN, i18n.SW, i18n.LG} {
				if Title(tc.ruleID, lang) == "" {
					t.Errorf("Title(%s) empty", tc.ruleID)
				}
			}
		})
		t.Run(tc.name+"/clean", func(t *testing.T) {
			eng, _ := setup(t, tc.clean)
			if found := scanOne(t, eng, ctx, tc.ruleID); len(found) != 0 {
				t.Fatalf("%s fired on benign data: %+v", tc.ruleID, found)
			}
		})
	}
}

// TestScanDeterministic re-scans identical data and asserts byte-stable output:
// the ADTC audit re-runs the submission and compares.
func TestScanDeterministic(t *testing.T) {
	f := fixture{member: member(2), txns: []ledger.Txn{
		txn("D-01", 1, 9, "credit", 672_000_000),
		txn("D-02", 1, 14, "credit", 672_000_000),
		txn("D-03", 2, 10, "credit", 672_000_000),
	}}
	run := func(t *testing.T) []Finding {
		eng, ctx := setup(t, f)
		out, err := eng.Scan(ctx, testStart, testEnd, nil)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return out
	}
	first := run(t)
	second := run(t)
	if len(first) == 0 {
		t.Fatal("expected findings from structuring-shaped data")
	}
	if len(first) != len(second) {
		t.Fatalf("run lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Describe(i18n.EN) != second[i].Describe(i18n.EN) ||
			first[i].Severity != second[i].Severity ||
			fmt.Sprint(first[i].TxnIDs) != fmt.Sprint(second[i].TxnIDs) {
			t.Errorf("finding %d differs between identical runs", i)
		}
	}
}

// TestR09AndR06Separation is the F-03 regression test: the cycling rule must
// not fire when only Country differs (because Country is the R06 field), and
// the cross-border rule must fire on the same data. Before F-03, both
// detectors fought over Txn.Country and either could silently suppress the
// other depending on which row visited the rule first.
func TestR09AndR06Separation(t *testing.T) {
	f := fixture{member: member(2), txns: []ledger.Txn{
		// One SY credit, one KP credit, one YE debit — three distinct Country
	// values, but CounterpartyAccount is empty for all. R09 should NOT
	// fire; R06 SHOULD fire on the SY/KP/YE watchlist countries.
	withCountry(withAccount(txn("X-01", 1, 10, "credit", 1_200_000_000), ""), "SY"),
		withCountry(withAccount(txn("X-02", 1, 11, "credit", 800_000_000), ""), "KP"),
		withCountry(withAccount(txn("X-03", 2, 16, "debit", 1_000_000_000), ""), "YE"),
	}}
	eng, ctx := setup(t, f)

	cycle := scanOne(t, eng, ctx, RuleMultiAcctCycle)
	if len(cycle) != 0 {
		t.Errorf("R09 fired on Country-only data: %+v — F-03 regression", cycle)
	}
	cross := scanOne(t, eng, ctx, RuleCrossBorder)
	if len(cross) != 1 {
		t.Errorf("R06 should fire on SY/KP/YE data, got %d findings", len(cross))
	}
}
