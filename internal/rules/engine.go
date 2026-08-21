// Package rules is the deterministic AML detection layer.
//
// Nothing in this package calls a language model, and that is the central design
// decision of the whole submission. A 1.5B-parameter model quantised to 4 bits
// is a competent writer and a poor adjudicator: ask it whether a deposit pattern
// constitutes structuring and it will be confidently wrong some fraction of the
// time, non-reproducibly, with no audit trail. Every flagging decision therefore
// comes from code here, over integer arithmetic, against thresholds the
// institution set. The model's only job downstream is to explain a finding this
// package already made.
//
// Practical consequences:
//   - the same ledger and the same policy always yield the same alerts
//   - every alert carries the transaction ids and the threshold it fired against
//   - the detection layer works with the model absent entirely
//
// The eight typologies below are the patterns FATF's guidance for the financial
// inclusion sector and Kenyan SACCO supervisory practice actually turn up in
// small deposit-taking institutions. They are intentionally not exotic.
package rules

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kanzu-agent/kanzu/internal/i18n"
	"github.com/kanzu-agent/kanzu/internal/ledger"
)

// Rule identifiers. Stable strings: they appear in stored alerts and in reports,
// so renaming one breaks historical records.
const (
	RuleStructuring  = "R01_STRUCTURING"
	RuleVelocity     = "R02_VELOCITY"
	RuleRoundAmount  = "R03_ROUND_AMOUNT"
	RuleDormantReact = "R04_DORMANT_REACTIVATION"
	RulePassThrough  = "R05_RAPID_PASSTHROUGH"
	RuleCrossBorder  = "R06_CROSS_BORDER_EXPOSURE"
	RuleKYCGap       = "R07_KYC_LIMIT_BREACH"
	RuleThresholdHug = "R08_THRESHOLD_HUGGING"
)

// All returns every rule id in evaluation order.
func All() []string {
	return []string{
		RuleStructuring, RuleVelocity, RuleRoundAmount, RuleDormantReact,
		RulePassThrough, RuleCrossBorder, RuleKYCGap, RuleThresholdHug,
	}
}

// Severity levels.
const (
	SevLow    = "low"
	SevMedium = "medium"
	SevHigh   = "high"
)

// Facts holds the structured numbers behind a finding. Keeping them structured
// rather than pre-formatted into a sentence is what allows the same finding to be
// rendered in English or Kiswahili without a translation step, and what lets the
// prompt builder hand the model numbers it cannot misread.
type Facts struct {
	Count          int
	TotalMinor     int64
	MaxMinor       int64
	ThresholdMinor int64
	BaselineMinor  int64
	Multiple       float64
	WindowHours    int
	DormantDays    int
	Countries      []string
	Currency       string
	KYCLevel       int
}

// Finding is one rule firing against one member.
type Finding struct {
	RuleID      string
	MemberID    string
	MemberName  string
	WindowStart time.Time
	WindowEnd   time.Time
	Severity    string
	Score       float64
	TxnIDs      []string
	Facts       Facts
}

// Title returns the typology name in the requested language.
func Title(ruleID string, lang i18n.Lang) string {
	if t, ok := titles[ruleID]; ok {
		if s, ok := t[lang]; ok {
			return s
		}
		return t[i18n.EN]
	}
	return ruleID
}

var titles = map[string]map[i18n.Lang]string{
	RuleStructuring: {
		i18n.EN: "Structuring (deposit splitting)",
		i18n.SW: "Kugawanya miamala (structuring)",
	},
	RuleVelocity: {
		i18n.EN: "Transaction velocity spike",
		i18n.SW: "Ongezeko la kasi ya miamala",
	},
	RuleRoundAmount: {
		i18n.EN: "Repeated round-figure amounts",
		i18n.SW: "Kurudia kiasi kilichokamilika",
	},
	RuleDormantReact: {
		i18n.EN: "Dormant account reactivation",
		i18n.SW: "Kufufuka kwa akaunti tulivu",
	},
	RulePassThrough: {
		i18n.EN: "Rapid pass-through of funds (layering)",
		i18n.SW: "Fedha kupita haraka (upangaji tabaka)",
	},
	RuleCrossBorder: {
		i18n.EN: "Higher-risk jurisdiction exposure",
		i18n.SW: "Mahusiano na nchi za hatari kubwa",
	},
	RuleKYCGap: {
		i18n.EN: "Activity beyond verified KYC tier",
		i18n.SW: "Shughuli zaidi ya kiwango cha utambulisho",
	},
	RuleThresholdHug: {
		i18n.EN: "Amounts clustered below reporting threshold",
		i18n.SW: "Viwango vilivyokusanyika chini ya kikomo cha kuripoti",
	},
}

// Describe renders a finding as one deterministic sentence in the requested
// language. No model involved: the operator sees the same evidence the report
// will cite, before any narration happens.
func (f Finding) Describe(lang i18n.Lang) string {
	cur := f.Facts.Currency
	if cur == "" {
		cur = "KES"
	}
	total := ledger.Money(f.Facts.TotalMinor, cur)
	thr := ledger.Money(f.Facts.ThresholdMinor, cur)
	base := ledger.Money(f.Facts.BaselineMinor, cur)
	maxAmt := ledger.Money(f.Facts.MaxMinor, cur)

	sw := lang == i18n.SW
	switch f.RuleID {
	case RuleStructuring:
		if sw {
			return fmt.Sprintf("Miamala %d ya jumla ya %s katika saa %d, kila mmoja chini ya kikomo cha %s, lakini jumla imevuka kikomo.",
				f.Facts.Count, total, f.Facts.WindowHours, thr)
		}
		return fmt.Sprintf("%d transactions totalling %s within %d hours; each individually below the %s threshold while the aggregate exceeds it.",
			f.Facts.Count, total, f.Facts.WindowHours, thr)
	case RuleVelocity:
		if sw {
			return fmt.Sprintf("Thamani ya %s katika saa %d ni mara %.1f ya kawaida ya mwanachama (%s).",
				total, f.Facts.WindowHours, f.Facts.Multiple, base)
		}
		return fmt.Sprintf("Window value of %s over %d hours is %.1fx the member's own trailing baseline of %s.",
			total, f.Facts.WindowHours, f.Facts.Multiple, base)
	case RuleRoundAmount:
		if sw {
			return fmt.Sprintf("Kiasi kilekile kilichokamilika cha %s kimerudiwa mara %d; jumla %s.",
				maxAmt, f.Facts.Count, total)
		}
		return fmt.Sprintf("The identical round figure %s repeats %d times, totalling %s.",
			maxAmt, f.Facts.Count, total)
	case RuleDormantReact:
		if sw {
			return fmt.Sprintf("Akaunti ilikaa tulivu siku %d, kisha ilipokea miamala %d ya jumla %s.",
				f.Facts.DormantDays, f.Facts.Count, total)
		}
		return fmt.Sprintf("Account was inactive for %d days, then recorded %d transactions totalling %s.",
			f.Facts.DormantDays, f.Facts.Count, total)
	case RulePassThrough:
		if sw {
			return fmt.Sprintf("Asilimia %.0f ya fedha zilizoingia zilitolewa tena katika saa %d (jumla %s).",
				f.Facts.Multiple, f.Facts.WindowHours, total)
		}
		return fmt.Sprintf("%.0f%% of incoming value left the account again within %d hours (total %s).",
			f.Facts.Multiple, f.Facts.WindowHours, total)
	case RuleCrossBorder:
		if sw {
			return fmt.Sprintf("Miamala %d ya jumla %s inahusisha nchi za hatari kubwa: %s.",
				f.Facts.Count, total, strings.Join(f.Facts.Countries, ", "))
		}
		return fmt.Sprintf("%d transactions totalling %s involve watchlisted jurisdictions: %s.",
			f.Facts.Count, total, strings.Join(f.Facts.Countries, ", "))
	case RuleKYCGap:
		if sw {
			return fmt.Sprintf("Kiwango cha utambulisho ni %d, lakini shughuli za %s zimevuka ukomo wa %s.",
				f.Facts.KYCLevel, total, thr)
		}
		return fmt.Sprintf("KYC tier is %d, yet activity of %s exceeds the tier ceiling of %s.",
			f.Facts.KYCLevel, total, thr)
	case RuleThresholdHug:
		if sw {
			return fmt.Sprintf("Miamala %d imepangwa kati ya asilimia 85 na 100 ya kikomo cha %s (kubwa zaidi %s).",
				f.Facts.Count, thr, maxAmt)
		}
		return fmt.Sprintf("%d transactions sit between 85%% and 100%% of the %s threshold (largest %s).",
			f.Facts.Count, thr, maxAmt)
	}
	return fmt.Sprintf("%d transactions totalling %s.", f.Facts.Count, total)
}

// Engine evaluates rules against the ledger.
type Engine struct {
	DB     *ledger.DB
	Policy ledger.Policy
}

// NewEngine loads policy and returns a ready engine.
func NewEngine(ctx context.Context, db *ledger.DB) (*Engine, error) {
	p, err := db.LoadPolicy(ctx)
	if err != nil {
		return nil, err
	}
	return &Engine{DB: db, Policy: p}, nil
}

// Scan evaluates the selected rules (all, when ruleIDs is empty) for every member
// with activity in [start, end) and persists the resulting alerts.
//
// Findings are ordered severity-desc then rule id then member id, so two runs
// over identical data produce byte-identical output. Reproducibility is not a
// nicety here: the ADTC audit re-runs the submission and compares.
func (e *Engine) Scan(ctx context.Context, start, end time.Time, ruleIDs []string) ([]Finding, error) {
	enabled := map[string]bool{}
	if len(ruleIDs) == 0 {
		for _, r := range All() {
			enabled[r] = true
		}
	} else {
		for _, r := range ruleIDs {
			enabled[strings.ToUpper(strings.TrimSpace(r))] = true
		}
	}

	txns, err := e.DB.TxnsBetween(ctx, start, end)
	if err != nil {
		return nil, err
	}
	byMember := map[string][]ledger.Txn{}
	order := []string{}
	for _, t := range txns {
		if _, seen := byMember[t.MemberID]; !seen {
			order = append(order, t.MemberID)
		}
		byMember[t.MemberID] = append(byMember[t.MemberID], t)
	}
	sort.Strings(order)

	currency := e.Policy.Str("currency", "KES")
	threshold := e.Policy.Int("internal_report_threshold_minor", 100_000_000)

	var out []Finding
	for _, memberID := range order {
		member, err := e.DB.Member(ctx, memberID)
		if err != nil {
			// A transaction referencing an unknown member is a data-quality
			// problem, not a reason to abandon the whole scan.
			continue
		}
		in := input{
			member:    member,
			txns:      byMember[memberID],
			start:     start,
			end:       end,
			policy:    e.Policy,
			db:        e.DB,
			currency:  currency,
			threshold: threshold,
		}
		for _, ruleID := range All() {
			if !enabled[ruleID] {
				continue
			}
			f, ok, err := e.evaluate(ctx, ruleID, in)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, f)
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := sevRank(out[i].Severity), sevRank(out[j].Severity)
		if si != sj {
			return si < sj
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].MemberID < out[j].MemberID
	})

	for i := range out {
		if _, err := e.DB.SaveAlert(ctx, out[i].toAlert()); err != nil {
			return nil, err
		}
	}
	if err := e.DB.Audit(ctx, "rules.engine", "scan",
		fmt.Sprintf("%s..%s", start.Format("2006-01-02"), end.Format("2006-01-02")),
		fmt.Sprintf("%d findings over %d transactions", len(out), len(txns))); err != nil {
		return nil, err
	}
	return out, nil
}

func (f Finding) toAlert() ledger.Alert {
	return ledger.Alert{
		RuleID:        f.RuleID,
		MemberID:      f.MemberID,
		WindowStart:   f.WindowStart,
		WindowEnd:     f.WindowEnd,
		Severity:      f.Severity,
		Score:         f.Score,
		AmountMinor:   f.Facts.TotalMinor,
		TxnCount:      f.Facts.Count,
		ThresholdUsed: f.Facts.ThresholdMinor,
		TxnIDs:        f.TxnIDs,
		Rationale:     f.Describe(i18n.EN),
	}
}

func sevRank(s string) int {
	switch s {
	case SevHigh:
		return 0
	case SevMedium:
		return 1
	default:
		return 2
	}
}

type input struct {
	member    ledger.Member
	txns      []ledger.Txn
	start     time.Time
	end       time.Time
	policy    ledger.Policy
	db        *ledger.DB
	currency  string
	threshold int64
}

func (e *Engine) evaluate(ctx context.Context, ruleID string, in input) (Finding, bool, error) {
	switch ruleID {
	case RuleStructuring:
		f, ok := detectStructuring(in)
		return f, ok, nil
	case RuleVelocity:
		return detectVelocity(ctx, in)
	case RuleRoundAmount:
		f, ok := detectRoundAmount(in)
		return f, ok, nil
	case RuleDormantReact:
		return detectDormantReactivation(ctx, in)
	case RulePassThrough:
		f, ok := detectPassThrough(in)
		return f, ok, nil
	case RuleCrossBorder:
		f, ok := detectCrossBorder(in)
		return f, ok, nil
	case RuleKYCGap:
		f, ok := detectKYCGap(in)
		return f, ok, nil
	case RuleThresholdHug:
		f, ok := detectThresholdHugging(in)
		return f, ok, nil
	}
	return Finding{}, false, nil
}

func (in input) base(ruleID, severity string, score float64) Finding {
	return Finding{
		RuleID:      ruleID,
		MemberID:    in.member.ID,
		MemberName:  in.member.Name,
		WindowStart: in.start,
		WindowEnd:   in.end,
		Severity:    severity,
		Score:       score,
		Facts:       Facts{Currency: in.currency, ThresholdMinor: in.threshold},
	}
}

// ── R01 structuring ──────────────────────────────────────────────────────────
//
// The classic pattern: a member who would trip the reporting threshold with one
// deposit makes several smaller ones instead. Detected as a rolling time window
// over same-direction transactions where every member of the group is below the
// threshold but the group total is not. The widest qualifying window wins, so one
// campaign produces one alert rather than one per sub-window.
func detectStructuring(in input) (Finding, bool) {
	window := time.Duration(in.policy.Int("structuring_window_hours", 72)) * time.Hour
	minTxns := int(in.policy.Int("structuring_min_txns", 3))

	credits := filterDirection(in.txns, true)
	if len(credits) < minTxns {
		return Finding{}, false
	}

	var bestIDs []string
	var bestTotal int64
	var bestCount int
	for i := range credits {
		var sum int64
		ids := make([]string, 0, len(credits)-i)
		for j := i; j < len(credits); j++ {
			if credits[j].TS.Sub(credits[i].TS) > window {
				break
			}
			if credits[j].AmountMinor >= in.threshold {
				// A transaction at or above the threshold is reportable on its
				// own; including it would make the aggregate finding redundant.
				continue
			}
			sum += credits[j].AmountMinor
			ids = append(ids, credits[j].ID)
		}
		if len(ids) >= minTxns && sum >= in.threshold && sum > bestTotal {
			bestTotal, bestCount = sum, len(ids)
			bestIDs = append([]string(nil), ids...)
		}
	}
	if bestCount == 0 {
		return Finding{}, false
	}

	f := in.base(RuleStructuring, SevHigh, ratio(bestTotal, in.threshold))
	f.TxnIDs = bestIDs
	f.Facts.Count = bestCount
	f.Facts.TotalMinor = bestTotal
	f.Facts.WindowHours = int(window / time.Hour)
	return f, true
}

// ── R02 velocity ─────────────────────────────────────────────────────────────
//
// Baselined against the member, not the institution. A market trader moving
// KES 400,000 a week is normal; the same volume from a member whose trailing
// average is KES 20,000 is not. Requires a minimum history so a new member's
// first active week cannot fire it.
func detectVelocity(ctx context.Context, in input) (Finding, bool, error) {
	windowHours := in.policy.Int("velocity_window_hours", 168)
	multiple := float64(in.policy.Int("velocity_multiple", 4))
	minBaseline := int(in.policy.Int("velocity_min_baseline_txns", 4))

	lookback := time.Duration(windowHours) * time.Hour
	baseline, err := in.db.Baseline(ctx, in.member.ID, in.start, lookback)
	if err != nil {
		return Finding{}, false, err
	}
	if baseline.TxnCount < minBaseline || baseline.TotalMinor <= 0 {
		return Finding{}, false, nil
	}

	total := sumAmounts(in.txns)
	if float64(total) < float64(baseline.TotalMinor)*multiple {
		return Finding{}, false, nil
	}

	observed := float64(total) / float64(baseline.TotalMinor)
	sev := SevMedium
	if observed >= multiple*2 {
		sev = SevHigh
	}
	f := in.base(RuleVelocity, sev, clamp01(observed/(multiple*3)))
	f.TxnIDs = idsOf(in.txns)
	f.Facts.Count = len(in.txns)
	f.Facts.TotalMinor = total
	f.Facts.BaselineMinor = baseline.TotalMinor
	f.Facts.Multiple = observed
	f.Facts.WindowHours = int(windowHours)
	return f, true, nil
}

// ── R03 repeated round figures ───────────────────────────────────────────────
//
// Genuine commerce produces untidy numbers. Repeated identical round figures are
// characteristic of a placement schedule rather than of trading receipts.
func detectRoundAmount(in input) (Finding, bool) {
	minRepeats := int(in.policy.Int("round_amount_min_repeats", 3))
	modulus := in.policy.Int("round_amount_modulus_minor", 1_000_000)
	if modulus <= 0 {
		return Finding{}, false
	}

	groups := map[int64][]ledger.Txn{}
	for _, t := range in.txns {
		if t.AmountMinor > 0 && t.AmountMinor%modulus == 0 {
			groups[t.AmountMinor] = append(groups[t.AmountMinor], t)
		}
	}
	// Deterministic winner: most repeats, then largest amount.
	amounts := make([]int64, 0, len(groups))
	for a := range groups {
		amounts = append(amounts, a)
	}
	sort.Slice(amounts, func(i, j int) bool {
		li, lj := len(groups[amounts[i]]), len(groups[amounts[j]])
		if li != lj {
			return li > lj
		}
		return amounts[i] > amounts[j]
	})
	if len(amounts) == 0 || len(groups[amounts[0]]) < minRepeats {
		return Finding{}, false
	}

	winner := amounts[0]
	group := groups[winner]
	total := sumAmounts(group)
	f := in.base(RuleRoundAmount, SevLow, clamp01(float64(len(group))/float64(minRepeats*3)))
	if total >= in.threshold {
		f.Severity = SevMedium
	}
	f.TxnIDs = idsOf(group)
	f.Facts.Count = len(group)
	f.Facts.TotalMinor = total
	f.Facts.MaxMinor = winner
	return f, true
}

// ── R04 dormant reactivation ─────────────────────────────────────────────────
//
// Dormant accounts are attractive precisely because they have a clean history.
// Fires on inactivity followed by material value.
func detectDormantReactivation(ctx context.Context, in input) (Finding, bool, error) {
	dormancyDays := in.policy.Int("dormancy_days", 180)
	pct := in.policy.Int("dormant_reactivation_pct", 50)

	last, ok, err := in.db.LastTxnBefore(ctx, in.member.ID, in.start)
	if err != nil {
		return Finding{}, false, err
	}
	if !ok {
		// No prior history at all is a new member, not a reactivation.
		return Finding{}, false, nil
	}
	idleDays := int(in.start.Sub(last).Hours() / 24)
	if int64(idleDays) < dormancyDays {
		return Finding{}, false, nil
	}

	total := sumAmounts(in.txns)
	trigger := in.threshold * pct / 100
	if total < trigger {
		return Finding{}, false, nil
	}

	sev := SevMedium
	if total >= in.threshold {
		sev = SevHigh
	}
	f := in.base(RuleDormantReact, sev, clamp01(float64(total)/float64(maxI64(in.threshold, 1))))
	f.TxnIDs = idsOf(in.txns)
	f.Facts.Count = len(in.txns)
	f.Facts.TotalMinor = total
	f.Facts.DormantDays = idleDays
	return f, true, nil
}

// ── R05 rapid pass-through ───────────────────────────────────────────────────
//
// Layering signature: value arrives and most of it leaves again almost at once,
// so the account is a conduit rather than a store of savings. A savings product
// with near-100% same-window outflow is not being used as a savings product.
func detectPassThrough(in input) (Finding, bool) {
	windowHours := in.policy.Int("passthrough_window_hours", 48)
	ratioPct := in.policy.Int("passthrough_ratio_pct", 80)
	window := time.Duration(windowHours) * time.Hour

	credits := filterDirection(in.txns, true)
	debits := filterDirection(in.txns, false)
	if len(credits) == 0 || len(debits) == 0 {
		return Finding{}, false
	}

	var bestIn, bestOut int64
	var bestIDs []string
	for _, c := range credits {
		var outflow int64
		ids := []string{c.ID}
		for _, dt := range debits {
			delta := dt.TS.Sub(c.TS)
			if delta < 0 || delta > window {
				continue
			}
			outflow += dt.AmountMinor
			ids = append(ids, dt.ID)
		}
		if outflow == 0 {
			continue
		}
		if outflow*100 >= c.AmountMinor*ratioPct && outflow > bestOut {
			bestIn, bestOut = c.AmountMinor, outflow
			bestIDs = append([]string(nil), ids...)
		}
	}
	if bestOut == 0 {
		return Finding{}, false
	}

	pct := float64(bestOut) / float64(maxI64(bestIn, 1)) * 100
	sev := SevMedium
	if bestIn >= in.threshold/2 {
		sev = SevHigh
	}
	f := in.base(RulePassThrough, sev, clamp01(pct/150))
	f.TxnIDs = bestIDs
	f.Facts.Count = len(bestIDs)
	f.Facts.TotalMinor = bestOut
	f.Facts.MaxMinor = bestIn
	f.Facts.Multiple = pct
	f.Facts.WindowHours = int(windowHours)
	return f, true
}

// ── R06 higher-risk jurisdiction exposure ────────────────────────────────────
//
// Presence on the institution's watchlist mandates enhanced due diligence; it is
// not itself an accusation. Severity stays medium unless value is material.
func detectCrossBorder(in input) (Finding, bool) {
	list := in.policy.Str("high_risk_countries", "")
	watch := map[string]bool{}
	for _, c := range strings.Split(list, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			watch[c] = true
		}
	}
	if len(watch) == 0 {
		return Finding{}, false
	}

	var hits []ledger.Txn
	seen := map[string]bool{}
	var countries []string
	for _, t := range in.txns {
		c := strings.ToUpper(strings.TrimSpace(t.Country))
		if c == "" || !watch[c] {
			continue
		}
		hits = append(hits, t)
		if !seen[c] {
			seen[c] = true
			countries = append(countries, c)
		}
	}
	if len(hits) == 0 {
		return Finding{}, false
	}
	sort.Strings(countries)

	total := sumAmounts(hits)
	sev := SevMedium
	if total >= in.threshold/2 || in.member.IsPEP {
		sev = SevHigh
	}
	f := in.base(RuleCrossBorder, sev, clamp01(float64(total)/float64(maxI64(in.threshold, 1))+0.3))
	f.TxnIDs = idsOf(hits)
	f.Facts.Count = len(hits)
	f.Facts.TotalMinor = total
	f.Facts.Countries = countries
	return f, true
}

// ── R07 activity beyond verified KYC tier ────────────────────────────────────
//
// The compliance failure that actually gets small institutions sanctioned is not
// missing an exotic typology; it is letting an unverified member transact at
// volume. Cheap to detect, and the finding is unambiguous.
func detectKYCGap(in input) (Finding, bool) {
	limit := in.policy.Int("kyc_tier1_limit_minor", 20_000_000)
	if in.member.KYCLevel >= 2 {
		return Finding{}, false
	}
	total := sumAmounts(in.txns)
	if total <= limit {
		return Finding{}, false
	}
	sev := SevMedium
	if in.member.KYCLevel == 0 {
		sev = SevHigh
	}
	f := in.base(RuleKYCGap, sev, clamp01(float64(total)/float64(maxI64(limit, 1))/3))
	f.TxnIDs = idsOf(in.txns)
	f.Facts.Count = len(in.txns)
	f.Facts.TotalMinor = total
	f.Facts.ThresholdMinor = limit
	f.Facts.KYCLevel = in.member.KYCLevel
	return f, true
}

// ── R08 threshold hugging ────────────────────────────────────────────────────
//
// Distinct from structuring: here the aggregate need not breach the threshold.
// Repeated amounts parked just underneath it indicate someone who knows where the
// line is. Low severity on its own; it earns its keep by co-occurring with R01.
func detectThresholdHugging(in input) (Finding, bool) {
	bandPct := in.policy.Int("structuring_band_pct", 85)
	lower := in.threshold * bandPct / 100

	var hits []ledger.Txn
	var maxAmt int64
	for _, t := range in.txns {
		if t.AmountMinor >= lower && t.AmountMinor < in.threshold {
			hits = append(hits, t)
			if t.AmountMinor > maxAmt {
				maxAmt = t.AmountMinor
			}
		}
	}
	if len(hits) < 2 {
		return Finding{}, false
	}
	total := sumAmounts(hits)
	f := in.base(RuleThresholdHug, SevMedium, clamp01(float64(len(hits))/6))
	f.TxnIDs = idsOf(hits)
	f.Facts.Count = len(hits)
	f.Facts.TotalMinor = total
	f.Facts.MaxMinor = maxAmt
	return f, true
}

// ── helpers ──────────────────────────────────────────────────────────────────

func filterDirection(txns []ledger.Txn, credit bool) []ledger.Txn {
	out := make([]ledger.Txn, 0, len(txns))
	for _, t := range txns {
		if t.IsCredit() == credit {
			out = append(out, t)
		}
	}
	return out
}

func sumAmounts(txns []ledger.Txn) int64 {
	var s int64
	for _, t := range txns {
		s += t.AmountMinor
	}
	return s
}

func idsOf(txns []ledger.Txn) []string {
	out := make([]string, 0, len(txns))
	for _, t := range txns {
		out = append(out, t.ID)
	}
	return out
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return clamp01(float64(a) / float64(b) / 2)
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
