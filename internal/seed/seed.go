// Package seed loads the demonstration ledger and the knowledge corpus.
//
// Transaction dates are stored in the fixtures as *day offsets* relative to seed
// time rather than as absolute dates. That is deliberate: a submission judged
// weeks after it was written must still answer "flag suspicious transactions from
// last week" with real data. Absolute dates in a fixture rot, and a demo that
// requires the reviewer to guess the right date range is a demo that fails.
package seed

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kanzu-agent/kanzu/internal/ledger"
)

// Report summarises a seeding run.
type Report struct {
	Members      int
	Accounts     int
	Transactions int
	Chunks       int
	Anchor       time.Time
}

// Load populates members, accounts and transactions from fixturesDir.
//
// Idempotent: members upsert and transactions insert-or-ignore on primary key, so
// re-seeding does not duplicate rows or manufacture a false velocity alert. The
// anchor date is truncated to midnight UTC so repeated runs on the same day
// produce identical timestamps.
func Load(ctx context.Context, db *ledger.DB, fixturesDir string, now time.Time) (Report, error) {
	var rep Report
	anchor := now.UTC().Truncate(24 * time.Hour)
	rep.Anchor = anchor

	memberPath := filepath.Join(fixturesDir, "members.csv")
	txnPath := filepath.Join(fixturesDir, "transactions.csv")

	members, err := readCSV(memberPath, []string{
		"member_id", "name", "joined_days_ago", "kyc_level", "risk_band", "is_pep", "dormant", "home_branch",
	})
	if err != nil {
		return rep, err
	}

	for i, row := range members {
		joinedDays, err := atoiField(row, "joined_days_ago", memberPath, i)
		if err != nil {
			return rep, err
		}
		kyc, err := atoiField(row, "kyc_level", memberPath, i)
		if err != nil {
			return rep, err
		}
		m := ledger.Member{
			ID:         strings.ToUpper(row["member_id"]),
			Name:       row["name"],
			JoinedAt:   anchor.AddDate(0, 0, -joinedDays),
			KYCLevel:   kyc,
			RiskBand:   strings.ToLower(orDefault(row["risk_band"], "low")),
			IsPEP:      truthy(row["is_pep"]),
			Dormant:    truthy(row["dormant"]),
			HomeBranch: row["home_branch"],
		}
		if m.ID == "" {
			return rep, fmt.Errorf("%s row %d: empty member_id", memberPath, i+2)
		}
		if err := db.UpsertMember(ctx, m); err != nil {
			return rep, err
		}
		rep.Members++

		// One savings account per member keeps the fixture readable; the schema
		// supports many.
		acct := "A-" + strings.TrimPrefix(m.ID, "M-")
		if err := db.UpsertAccount(ctx, acct, m.ID, "savings", "UGX", m.JoinedAt); err != nil {
			return rep, err
		}
		rep.Accounts++
	}

	txns, err := readCSV(txnPath, []string{
		"txn_id", "member_id", "day_offset", "time", "direction", "channel",
		"amount", "currency", "counterparty", "country", "narrative",
	})
	if err != nil {
		return rep, err
	}

	for i, row := range txns {
		offset, err := atoiField(row, "day_offset", txnPath, i)
		if err != nil {
			return rep, err
		}
		clock, err := parseClock(row["time"])
		if err != nil {
			return rep, fmt.Errorf("%s row %d: %w", txnPath, i+2, err)
		}
		minor, err := parseMoneyMinor(row["amount"])
		if err != nil {
			return rep, fmt.Errorf("%s row %d: amount %q: %w", txnPath, i+2, row["amount"], err)
		}

		// offset is expressed as days before the anchor, so a positive fixture
		// value moves backwards in time.
		day := anchor.AddDate(0, 0, -offset)
		ts := day.Add(clock)

		memberID := strings.ToUpper(row["member_id"])
		t := ledger.Txn{
			ID:           row["txn_id"],
			AccountID:    "A-" + strings.TrimPrefix(memberID, "M-"),
			MemberID:     memberID,
			TS:           ts,
			Direction:    strings.ToLower(row["direction"]),
			Channel:      strings.ToLower(row["channel"]),
			AmountMinor:  minor,
			Currency:     orDefault(row["currency"], "UGX"),
			Counterparty: row["counterparty"],
			Country:      strings.ToUpper(row["country"]),
			Narrative:    row["narrative"],
			Reference:    row["txn_id"],
		}
		if t.Direction != "credit" && t.Direction != "debit" {
			return rep, fmt.Errorf("%s row %d: direction must be credit or debit, got %q",
				txnPath, i+2, row["direction"])
		}
		if err := db.InsertTxn(ctx, t); err != nil {
			return rep, err
		}
		rep.Transactions++
	}

	if err := db.Audit(ctx, "seed", "load.fixtures", fixturesDir,
		fmt.Sprintf("anchor=%s members=%d txns=%d", anchor.Format("2006-01-02"), rep.Members, rep.Transactions)); err != nil {
		return rep, err
	}
	return rep, nil
}

// readCSV reads a header-mapped CSV and verifies the required columns exist, so a
// silently-renamed column fails loudly instead of seeding zeroes.
func readCSV(path string, required []string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fixture %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header of %s: %w", path, err)
	}
	index := map[string]int{}
	for i, h := range header {
		index[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	var missing []string
	for _, col := range required {
		if _, ok := index[col]; !ok {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s missing required column(s): %s", path, strings.Join(missing, ", "))
	}

	var out []map[string]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		row := map[string]string{}
		for col, i := range index {
			if i < len(rec) {
				row[col] = strings.TrimSpace(rec[i])
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// parseMoneyMinor converts "240000.00" or "240,000" to integer minor units.
//
// String-then-integer rather than float: parsing money through a float64 and
// multiplying by 100 introduces the exact sub-cent drift that an AML rule
// comparing an aggregate against a threshold cannot tolerate.
func parseMoneyMinor(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, errors.New("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	var cents int64
	if hasFrac {
		switch len(frac) {
		case 0:
		case 1:
			d, err := strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return 0, err
			}
			cents = d * 10
		default:
			d, err := strconv.ParseInt(frac[:2], 10, 64)
			if err != nil {
				return 0, err
			}
			cents = d
		}
	}
	total := units*100 + cents
	if neg {
		total = -total
	}
	return total, nil
}

func parseClock(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 9 * time.Hour, nil
	}
	parts := strings.Split(s, ":")
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	m := 0
	if len(parts) > 1 {
		if m, err = strconv.Atoi(parts[1]); err != nil || m < 0 || m > 59 {
			return 0, fmt.Errorf("invalid time %q", s)
		}
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

func atoiField(row map[string]string, col, path string, i int) (int, error) {
	v := strings.TrimSpace(row[col])
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s row %d: %s must be an integer, got %q", path, i+2, col, v)
	}
	return n, nil
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "ndiyo":
		return true
	}
	return false
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
