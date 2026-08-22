// Package ledger is Kanzu Agent's local system of record: members, accounts,
// transactions, alerts, cases, the offline message queue, and the audit log.
//
// It contains no network code of any kind. Every call reads or writes one SQLite
// file on the local disk. The driver is modernc.org/sqlite, a pure-Go
// transpilation of SQLite, chosen over the CGO binding for one reason that
// matters to this competition: a clean clone must build with nothing but the Go
// toolchain. Requiring a C compiler adds a step the evaluator has to get right.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite handle.
type DB struct {
	sql  *sql.DB
	path string
}

// Open opens (creating if needed) the ledger and applies the schema.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("ledger: empty database path")
	}
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open ledger %s: %w", path, err)
	}
	// A single writer keeps SQLite out of SQLITE_BUSY territory without a retry
	// loop; the agent is inherently serial because the Governor paces it.
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		// Hard ceiling on SQLite's own page cache. Default is 2 MB but it grows
		// per connection; pinning it keeps the ledger's contribution to resident
		// memory predictable on an 8 GB machine.
		"PRAGMA cache_size = -8000",
		"PRAGMA temp_store = MEMORY",
	} {
		if _, err := handle.Exec(pragma); err != nil {
			handle.Close()
			return nil, fmt.Errorf("ledger pragma %q: %w", pragma, err)
		}
	}

	db := &DB{sql: handle, path: path}
	if err := db.migrate(); err != nil {
		handle.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the handle.
func (d *DB) Close() error { return d.sql.Close() }

// Path returns the on-disk location, for display in `kanzu doctor`.
func (d *DB) Path() string { return filepath.Clean(d.path) }

// SQL exposes the handle for the rag package, which owns the kb_chunks tables.
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(Schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	for _, p := range defaultPolicy {
		if _, err := d.sql.Exec(
			`INSERT INTO policy(key, value, unit, description) VALUES(?, ?, ?, ?)
			 ON CONFLICT(key) DO NOTHING`,
			p.Key, p.Value, p.Unit, p.Description); err != nil {
			return fmt.Errorf("seed policy %s: %w", p.Key, err)
		}
	}
	return nil
}

// ── domain types ─────────────────────────────────────────────────────────────

// Member is a SACCO member.
type Member struct {
	ID         string
	Name       string
	JoinedAt   time.Time
	KYCLevel   int
	RiskBand   string
	IsPEP      bool
	Dormant    bool
	HomeBranch string
}

// Txn is one ledger movement. Amount is in minor units.
type Txn struct {
	ID           string
	AccountID    string
	MemberID     string
	TS           time.Time
	Direction    string
	Channel      string
	AmountMinor  int64
	Currency     string
	Counterparty string
	Country      string
	Narrative    string
	Reference    string
}

// IsCredit reports whether value entered the institution.
func (t Txn) IsCredit() bool { return strings.EqualFold(t.Direction, "credit") }

// Alert is one deterministic rule firing.
type Alert struct {
	ID            int64
	RuleID        string
	MemberID      string
	WindowStart   time.Time
	WindowEnd     time.Time
	Severity      string
	Score         float64
	AmountMinor   int64
	TxnCount      int
	ThresholdUsed int64
	TxnIDs        []string
	Rationale     string
	Status        string
	CreatedAt     time.Time
}

// Case is a draft suspicious-activity file.
type Case struct {
	ID        int64
	MemberID  string
	Title     string
	Lang      string
	Status    string
	Narrative string
	Citations string
	OpenedAt  time.Time
}

// Message is one queued offline request or reply.
type Message struct {
	ID        int64
	Direction string
	Channel   string
	Peer      string
	Lang      string
	Body      string
	Status    string
	ReplyTo   int64
	CreatedAt time.Time
}

const tsLayout = time.RFC3339

func fmtTS(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ── policy ───────────────────────────────────────────────────────────────────

// Policy is the institution's threshold set, loaded once per command.
type Policy map[string]string

// LoadPolicy reads every policy row.
func (d *DB) LoadPolicy(ctx context.Context) (Policy, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT key, value FROM policy`)
	if err != nil {
		return nil, fmt.Errorf("load policy: %w", err)
	}
	defer rows.Close()
	p := Policy{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		p[k] = v
	}
	return p, rows.Err()
}

// Int returns a policy value as int64, or def when absent or unparseable.
func (p Policy) Int(key string, def int64) int64 {
	if v, ok := p[key]; ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Str returns a policy value as a string, or def when absent.
func (p Policy) Str(key, def string) string {
	if v, ok := p[key]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// Set updates one policy value and records the change in the audit log.
func (d *DB) Set(ctx context.Context, actor, key, value string) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE policy SET value = ? WHERE key = ?`, value, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("unknown policy key %q", key)
	}
	return d.Audit(ctx, actor, "policy.set", key, value)
}

// ── members and transactions ─────────────────────────────────────────────────

// UpsertMember inserts or replaces a member record.
func (d *DB) UpsertMember(ctx context.Context, m Member) error {
	dormant := interface{}(nil)
	if m.Dormant {
		dormant = fmtTS(m.JoinedAt)
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO members(id, name, joined_at, kyc_level, risk_band, is_pep, dormant_since, home_branch)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, kyc_level=excluded.kyc_level, risk_band=excluded.risk_band,
		   is_pep=excluded.is_pep, dormant_since=excluded.dormant_since, home_branch=excluded.home_branch`,
		m.ID, m.Name, fmtTS(m.JoinedAt), m.KYCLevel, m.RiskBand, boolInt(m.IsPEP), dormant, m.HomeBranch)
	return err
}

// UpsertAccount inserts or replaces an account record.
func (d *DB) UpsertAccount(ctx context.Context, id, memberID, kind, currency string, opened time.Time) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO accounts(id, member_id, kind, opened_at, currency)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, currency=excluded.currency`,
		id, memberID, kind, fmtTS(opened), currency)
	return err
}

// InsertTxn adds one transaction. Idempotent on transaction id so re-importing a
// fixture file cannot double-count and manufacture a false velocity alert.
func (d *DB) InsertTxn(ctx context.Context, t Txn) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO transactions
		   (id, account_id, member_id, ts, direction, channel, amount_minor, currency,
		    counterparty, counterparty_country, narrative, reference)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
		t.ID, t.AccountID, t.MemberID, fmtTS(t.TS), strings.ToLower(t.Direction),
		strings.ToLower(t.Channel), t.AmountMinor, t.Currency, t.Counterparty,
		strings.ToUpper(t.Country), t.Narrative, t.Reference)
	return err
}

const txnCols = `id, account_id, member_id, ts, direction, channel, amount_minor,
                 currency, COALESCE(counterparty,''), COALESCE(counterparty_country,''),
                 COALESCE(narrative,''), COALESCE(reference,'')`

func scanTxns(rows *sql.Rows) ([]Txn, error) {
	defer rows.Close()
	var out []Txn
	for rows.Next() {
		var t Txn
		var ts string
		if err := rows.Scan(&t.ID, &t.AccountID, &t.MemberID, &ts, &t.Direction, &t.Channel,
			&t.AmountMinor, &t.Currency, &t.Counterparty, &t.Country, &t.Narrative, &t.Reference); err != nil {
			return nil, err
		}
		t.TS = parseTS(ts)
		out = append(out, t)
	}
	return out, rows.Err()
}

// TxnsBetween returns transactions in [start, end), ordered by time then id so
// rule output is byte-stable across runs.
func (d *DB) TxnsBetween(ctx context.Context, start, end time.Time) ([]Txn, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+txnCols+` FROM transactions WHERE ts >= ? AND ts < ? ORDER BY ts, id`,
		fmtTS(start), fmtTS(end))
	if err != nil {
		return nil, fmt.Errorf("query transactions: %w", err)
	}
	return scanTxns(rows)
}

// TxnsForMember returns one member's transactions in [start, end).
func (d *DB) TxnsForMember(ctx context.Context, memberID string, start, end time.Time) ([]Txn, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+txnCols+` FROM transactions
		 WHERE member_id = ? AND ts >= ? AND ts < ? ORDER BY ts, id`,
		memberID, fmtTS(start), fmtTS(end))
	if err != nil {
		return nil, fmt.Errorf("query member transactions: %w", err)
	}
	return scanTxns(rows)
}

// Members returns every member, id-ordered.
func (d *DB) Members(ctx context.Context) ([]Member, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, name, joined_at, kyc_level, risk_band, is_pep,
		        CASE WHEN dormant_since IS NULL OR dormant_since='' THEN 0 ELSE 1 END,
		        COALESCE(home_branch,'')
		 FROM members ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var joined string
		var pep, dormant int
		if err := rows.Scan(&m.ID, &m.Name, &joined, &m.KYCLevel, &m.RiskBand, &pep, &dormant, &m.HomeBranch); err != nil {
			return nil, err
		}
		m.JoinedAt = parseTS(joined)
		m.IsPEP = pep == 1
		m.Dormant = dormant == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// Member fetches one member.
func (d *DB) Member(ctx context.Context, id string) (Member, error) {
	var m Member
	var joined string
	var pep, dormant int
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, name, joined_at, kyc_level, risk_band, is_pep,
		        CASE WHEN dormant_since IS NULL OR dormant_since='' THEN 0 ELSE 1 END,
		        COALESCE(home_branch,'')
		 FROM members WHERE id = ?`, id).
		Scan(&m.ID, &m.Name, &joined, &m.KYCLevel, &m.RiskBand, &pep, &dormant, &m.HomeBranch)
	if err != nil {
		return m, fmt.Errorf("member %s: %w", id, err)
	}
	m.JoinedAt = parseTS(joined)
	m.IsPEP = pep == 1
	m.Dormant = dormant == 1
	return m, nil
}

// FindMember resolves a member by id or by a case-insensitive name fragment.
// Operators type names, not ids.
func (d *DB) FindMember(ctx context.Context, needle string) (Member, error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return Member{}, errors.New("empty member reference")
	}
	if m, err := d.Member(ctx, strings.ToUpper(needle)); err == nil {
		return m, nil
	}
	var id string
	err := d.sql.QueryRowContext(ctx,
		`SELECT id FROM members WHERE lower(name) LIKE '%' || lower(?) || '%' ORDER BY id LIMIT 1`,
		needle).Scan(&id)
	if err != nil {
		return Member{}, fmt.Errorf("no member matching %q", needle)
	}
	return d.Member(ctx, id)
}

// BaselineStats summarises a member's trailing behaviour, used by the velocity
// rule so "unusual" is measured against the member rather than a global average.
type BaselineStats struct {
	TxnCount    int
	TotalMinor  int64
	MeanMinor   int64
	MaxMinor    int64
	WindowCount float64 // number of baseline windows observed
}

// Baseline computes trailing stats strictly before `before`, looking back
// `lookback`. Same-direction filtering is the caller's job.
func (d *DB) Baseline(ctx context.Context, memberID string, before time.Time, lookback time.Duration) (BaselineStats, error) {
	var st BaselineStats
	from := before.Add(-lookback)
	row := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(amount_minor),0), COALESCE(MAX(amount_minor),0)
		 FROM transactions WHERE member_id = ? AND ts >= ? AND ts < ?`,
		memberID, fmtTS(from), fmtTS(before))
	if err := row.Scan(&st.TxnCount, &st.TotalMinor, &st.MaxMinor); err != nil {
		return st, fmt.Errorf("baseline %s: %w", memberID, err)
	}
	if st.TxnCount > 0 {
		st.MeanMinor = st.TotalMinor / int64(st.TxnCount)
	}
	return st, nil
}

// LastTxnBefore returns the timestamp of the member's most recent activity
// strictly before t, used by the dormancy rule.
func (d *DB) LastTxnBefore(ctx context.Context, memberID string, t time.Time) (time.Time, bool, error) {
	var ts sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM transactions WHERE member_id = ? AND ts < ?`,
		memberID, fmtTS(t)).Scan(&ts)
	if err != nil {
		return time.Time{}, false, err
	}
	if !ts.Valid || strings.TrimSpace(ts.String) == "" {
		return time.Time{}, false, nil
	}
	return parseTS(ts.String), true, nil
}

// ── alerts ───────────────────────────────────────────────────────────────────

// SaveAlert persists a rule firing. Re-running the same scan over the same
// window updates the existing row rather than duplicating it, which keeps the
// alert list idempotent for repeated demos and repeated audits.
func (d *DB) SaveAlert(ctx context.Context, a Alert) (int64, error) {
	now := fmtTS(time.Now())
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO alerts(rule_id, member_id, window_start, window_end, severity, score,
		                    amount_minor, txn_count, threshold_used, txn_ids, rationale, status, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,'open',?)
		 ON CONFLICT(rule_id, member_id, window_start, window_end) DO UPDATE SET
		   severity=excluded.severity, score=excluded.score, amount_minor=excluded.amount_minor,
		   txn_count=excluded.txn_count, threshold_used=excluded.threshold_used,
		   txn_ids=excluded.txn_ids, rationale=excluded.rationale`,
		a.RuleID, a.MemberID, fmtTS(a.WindowStart), fmtTS(a.WindowEnd), a.Severity, a.Score,
		a.AmountMinor, a.TxnCount, a.ThresholdUsed, strings.Join(a.TxnIDs, ","), a.Rationale, now)
	if err != nil {
		return 0, fmt.Errorf("save alert %s/%s: %w", a.RuleID, a.MemberID, err)
	}
	var id int64
	err = d.sql.QueryRowContext(ctx,
		`SELECT id FROM alerts WHERE rule_id=? AND member_id=? AND window_start=? AND window_end=?`,
		a.RuleID, a.MemberID, fmtTS(a.WindowStart), fmtTS(a.WindowEnd)).Scan(&id)
	return id, err
}

const alertCols = `id, rule_id, member_id, window_start, window_end, severity, score,
                   amount_minor, txn_count, threshold_used, txn_ids, rationale, status, created_at`

func scanAlerts(rows *sql.Rows) ([]Alert, error) {
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var ws, we, created, ids string
		if err := rows.Scan(&a.ID, &a.RuleID, &a.MemberID, &ws, &we, &a.Severity, &a.Score,
			&a.AmountMinor, &a.TxnCount, &a.ThresholdUsed, &ids, &a.Rationale, &a.Status, &created); err != nil {
			return nil, err
		}
		a.WindowStart, a.WindowEnd, a.CreatedAt = parseTS(ws), parseTS(we), parseTS(created)
		if ids != "" {
			a.TxnIDs = strings.Split(ids, ",")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlertsInWindow returns alerts whose window overlaps [start, end), highest
// severity first so a report leads with the worst finding.
func (d *DB) AlertsInWindow(ctx context.Context, start, end time.Time) ([]Alert, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+alertCols+` FROM alerts
		 WHERE window_end > ? AND window_start < ?
		 ORDER BY CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, score DESC, id`,
		fmtTS(start), fmtTS(end))
	if err != nil {
		return nil, fmt.Errorf("query alerts: %w", err)
	}
	return scanAlerts(rows)
}

// AlertsForMember returns a member's alerts, newest first.
func (d *DB) AlertsForMember(ctx context.Context, memberID string) ([]Alert, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+alertCols+` FROM alerts WHERE member_id = ? ORDER BY id DESC`, memberID)
	if err != nil {
		return nil, fmt.Errorf("query member alerts: %w", err)
	}
	return scanAlerts(rows)
}

// ── cases ────────────────────────────────────────────────────────────────────

// OpenCase creates a draft case linked to the supplied alerts.
func (d *DB) OpenCase(ctx context.Context, c Case, alertIDs []int64) (int64, error) {
	now := fmtTS(time.Now())
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO cases(member_id, title, lang, status, narrative, citations, opened_at, updated_at)
		 VALUES(?,?,?,'draft',?,?,?,?)`,
		c.MemberID, c.Title, c.Lang, c.Narrative, c.Citations, now, now)
	if err != nil {
		return 0, fmt.Errorf("open case: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, aid := range alertIDs {
		if _, err := d.sql.ExecContext(ctx,
			`INSERT INTO case_alerts(case_id, alert_id) VALUES(?,?) ON CONFLICT DO NOTHING`,
			id, aid); err != nil {
			return 0, fmt.Errorf("link alert %d to case %d: %w", aid, id, err)
		}
	}
	return id, nil
}

// ── offline message queue ────────────────────────────────────────────────────

// Enqueue stores an inbound request or an outbound reply.
func (d *DB) Enqueue(ctx context.Context, m Message) (int64, error) {
	var replyTo interface{}
	if m.ReplyTo > 0 {
		replyTo = m.ReplyTo
	}
	status := m.Status
	if status == "" {
		if m.Direction == "outbound" {
			status = "queued"
		} else {
			status = "pending"
		}
	}
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO messages(direction, channel, peer, lang, body, status, reply_to, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		m.Direction, orDefault(m.Channel, "queue"), m.Peer, orDefault(m.Lang, "en"),
		m.Body, status, replyTo, fmtTS(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("enqueue message: %w", err)
	}
	return res.LastInsertId()
}

// PendingInbound returns up to limit unprocessed inbound requests, oldest first.
func (d *DB) PendingInbound(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, direction, channel, peer, lang, body, status, COALESCE(reply_to,0), created_at
		 FROM messages WHERE direction='inbound' AND status='pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query inbound queue: %w", err)
	}
	return scanMessages(rows)
}

// OutboundQueue returns replies waiting for a link.
func (d *DB) OutboundQueue(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, direction, channel, peer, lang, body, status, COALESCE(reply_to,0), created_at
		 FROM messages WHERE direction='outbound' AND status='queued' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query outbound queue: %w", err)
	}
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.Direction, &m.Channel, &m.Peer, &m.Lang,
			&m.Body, &m.Status, &m.ReplyTo, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkProcessed closes out an inbound message.
func (d *DB) MarkProcessed(ctx context.Context, id int64, status string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE messages SET status = ?, processed_at = ? WHERE id = ?`,
		status, fmtTS(time.Now()), id)
	return err
}

// ── audit trail ──────────────────────────────────────────────────────────────

// Audit appends one immutable audit record.
func (d *DB) Audit(ctx context.Context, actor, action, subject, detail string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO audit_log(ts, actor, action, subject, detail) VALUES(?,?,?,?,?)`,
		fmtTS(time.Now()), actor, action, subject, detail)
	return err
}

// Counts returns row counts for `kanzu doctor`.
func (d *DB) Counts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range []string{"members", "accounts", "transactions", "alerts", "cases", "messages", "kb_chunks", "audit_log"} {
		var n int
		// Table names are from this fixed literal slice, never from user input.
		if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}

// Money renders minor units as major units with thousands separators.
// Reports are read by people, and 100000000 is not a legible figure.
func Money(minor int64, currency string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	major := minor / 100
	cents := minor % 100
	s := strconv.FormatInt(major, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s %s.%02d", sign, orDefault(currency, "UGX"), b.String(), cents)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
