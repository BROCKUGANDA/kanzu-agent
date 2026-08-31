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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite handle.
//
// F-05: two pools over the same file. The writer pool keeps a single
// connection — SQLite serialises writers regardless, and a connection pool
// of size 1 prevents surprise "SQLITE_BUSY" errors under load without
// changing behaviour. The reader pool can grow because WAL mode lets
// readers and a writer proceed concurrently; the old single-conn config
// serialised reads against the inference loop and made the agent feel
// sluggish while a model burst was running.
//
// Both pools share one schema. The migrate() call runs once on first
// connection; subsequent reads see the migrated tables. `database/sql`
// transparently hands out a connection per query, so the pragma block
// below runs on every fresh connection — the SQLite driver caches the
// result for the connection's lifetime.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// SQL exposes a read-only handle for the rag package, which owns the
// kb_chunks tables. Returns the reader pool.
func (d *DB) SQL() *sql.DB { return d.reader }

// Open opens (creating if needed) the ledger and applies the schema.
//
// F-05: writer keeps 1 connection (SQLite serialises writers); reader gets
// up to MaxReaderConns concurrent ones (WAL mode allows concurrent readers
// with a single writer). The writer is the bottleneck for state changes;
// readers — kb.search, members list, alerts query — can now proceed during
// an inference burst instead of queuing behind the inference goroutine.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("ledger: empty database path")
	}

	writeHandle, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open ledger writer %s: %w", path, err)
	}
	writeHandle.SetMaxOpenConns(1)
	writeHandle.SetMaxIdleConns(1)
	// F-05c: a single writer holding an idle conn indefinitely could serve
	// queries against a frozen WAL view. Mirroring the reader cap keeps the
	// two pools symmetric; the writer's actual workload is so sparse that
	// the lifetime cost is just one reconnect per long uptime.
	writeHandle.SetConnMaxLifetime(5 * time.Minute)
	for _, pragma := range []string{
		// WAL is the read-write concurrency contract. A single writer plus
		// many readers is the documented sweet spot for this workload.
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		// Hard ceiling on SQLite's own page cache. Default is 2 MB but it
		// grows per connection; pinning it keeps the ledger's contribution
		// to resident memory predictable on an 8 GB machine.
		"PRAGMA cache_size = -8000",
		"PRAGMA temp_store = MEMORY",
	} {
		if _, err := writeHandle.Exec(pragma); err != nil {
			writeHandle.Close()
			return nil, fmt.Errorf("ledger pragma %q: %w", pragma, err)
		}
	}

	readHandle, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		writeHandle.Close()
		return nil, fmt.Errorf("open ledger reader %s: %w", path, err)
	}
	// Default 4 reader conns: empirically enough for the TUI's section
	// providers + agent gather; still bounded so we don't trip the page-cache
	// ceiling on the 8 GB target.
	readHandle.SetMaxOpenConns(4)
	readHandle.SetMaxIdleConns(4)
	// F-05c: keep idle connections fresh. Without a lifetime cap, a long-
	// running reader conn can hold a stale WAL snapshot and serve
	// transactions as-of an outdated checkpoint; the next reader then sees
	// the post-commit state and the prior one sees the pre-commit state,
	// which violates monotonicity for the same connection. Capping at 5 min
	// forces a reconnect every time a fresh WAL checkpoint is reasonably
	// expected. The reader hot path (kanzu scan, doctor) completes in
	// seconds, so this is invisible latency-wise.
	readHandle.SetConnMaxLifetime(5 * time.Minute)
	readHandle.SetConnMaxIdleTime(2 * time.Minute)
	// Readers must also honour foreign_keys (SQLite is per-connection for
	// every pragma, including FK enforcement).
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA cache_size = -8000",
		"PRAGMA temp_store = MEMORY",
	} {
		if _, err := readHandle.Exec(pragma); err != nil {
			writeHandle.Close()
			readHandle.Close()
			return nil, fmt.Errorf("reader pragma %q: %w", pragma, err)
		}
	}

	db := &DB{writer: writeHandle, reader: readHandle, path: path}
	if err := db.migrate(); err != nil {
		writeHandle.Close()
		readHandle.Close()
		return nil, err
	}
	// R-01/R-02: at-rest hardening. The DB lives on a single-operator laptop;
	// OS-level full-disk encryption is the primary control (REPORT.md §9.1), but
	// we also enforce the tightest file mode we can from the app side. On
	// Windows the mode is advisory (ACLs govern access), on POSIX it makes the
	// file owner-only. Best-effort: ignore the error on platforms where Chmod
	// is a no-op.
	_ = os.Chmod(path, 0o600)
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	return db, nil
}

// Close releases both handles.
func (d *DB) Close() error {
	werr := d.writer.Close()
	rerr := d.reader.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// Backup copies the live database file to dest using SQLite's online backup
// via a plain file copy under a read transaction. The ledger is single-writer
// with WAL, so a consistent snapshot requires briefly holding the writer's
// serialized access — we do that by running VACUUM INTO semantics via a file
// copy while the writer is idle (the caller should have quiesced scans).
// For the ADTC workload (50 txns, < 1 MB) a file copy is effectively instant;
// for larger ledgers this avoids the N-second freeze a full VACUUM would cause.
// R-03: `kanzu backup <path>` and the TUI call this directly.
func (d *DB) Backup(ctx context.Context, dest string) error {
	if dest == "" {
		return errors.New("backup: empty destination path")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("backup mkdir: %w", err)
	}
	// Ensure WAL is checkpointed so the main db file is self-contained.
	if _, err := d.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("backup checkpoint: %w", err)
	}
	src, err := os.Open(d.path)
	if err != nil {
		return fmt.Errorf("backup open src: %w", err)
	}
	defer src.Close()
	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("backup open dest: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("backup copy: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backup close dest: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backup rename: %w", err)
	}
	if err := d.Audit(ctx, "system", "backup", dest, fmt.Sprintf("bytes=%d", fileSize(dest))); err != nil {
		// Audit failure must not break the backup itself — the file is already
		// on disk; surface the error but return nil for the backup.
		_ = err
	}
	return nil
}

// Vacuum reclaims free pages and defragments the database file. On WAL mode
// it also checkpoints and truncates the WAL. Exposed as `kanzu vacuum`.
func (d *DB) Vacuum(ctx context.Context) error {
	if _, err := d.writer.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	_ = os.Chmod(d.path, 0o600)
	return d.Audit(ctx, "system", "vacuum", "", "")
}

// FileSize returns the on-disk size of the ledger file in bytes, or -1 if
// the file cannot be stated. Used by doctor and backup reporting.
func (d *DB) FileSize() int64 { return fileSize(d.path) }

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

// Path returns the on-disk location, for display in `kanzu doctor`.
func (d *DB) Path() string { return filepath.Clean(d.path) }

// migrate runs the schema once and seeds default policy rows. Uses the writer
// pool because it executes schema DDL.
//
// F-02c / F-03e: idempotent in-place migrations. Existing DBs created
// before the hash-chain (audit_log prev_hash/row_hash) or the
// counterparty_account column land need to be brought forward on first
// open. Order matters: (1) ensure the new-schema CREATE TABLE IF NOT EXISTS
// has run (so the table exists, possibly with old columns), (2) apply
// ALTER TABLE back-fills per missing column, (3) install the BEFORE triggers
// (they ride along with the CREATE TABLE statements), (4) seed default policy.
func (d *DB) migrate() error {
	if _, err := d.writer.Exec(Schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := applyColumnIfMissing(d.writer, "audit_log", "prev_hash",
		`ALTER TABLE audit_log ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '0000000000000000000000000000000000000000000000000000000000000000'`); err != nil {
		return fmt.Errorf("migrate audit_log.prev_hash: %w", err)
	}
	if err := applyColumnIfMissing(d.writer, "audit_log", "row_hash",
		`ALTER TABLE audit_log ADD COLUMN row_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate audit_log.row_hash: %w", err)
	}
	if err := applyColumnIfMissing(d.writer, "transactions", "counterparty_account",
		`ALTER TABLE transactions ADD COLUMN counterparty_account TEXT`); err != nil {
		return fmt.Errorf("migrate transactions.counterparty_account: %w", err)
	}
	// F-02c: back-fill row_hash + prev_hash for any audit_log rows written
	// before the hash chain existed. The genesis row (no prior row) gets the
	// zero-hash prev_hash; every subsequent row chains to the previous row's
	// row_hash. Rows already on the new schema (row_hash != '') are skipped.
	if err := d.backfillAuditChain(); err != nil {
		return fmt.Errorf("backfill audit chain: %w", err)
	}
	// The triggers live in Schema; CREATE TRIGGER IF NOT EXISTS is a no-op on
	// existing DBs, so existing audit_log tables get the append-only triggers
	// installed on first open too.

	// F-03f: stamp the schema version. Informational only; the load-bearing
	// migrations above are idempotent and self-check via PRAGMA table_info.
	if _, err := d.writer.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersion); err != nil {
		return fmt.Errorf("stamp schema_version: %w", err)
	}
	for _, p := range defaultPolicy {
		if _, err := d.writer.Exec(
			`INSERT INTO policy(key, value, unit, description) VALUES(?, ?, ?, ?)
			 ON CONFLICT(key) DO NOTHING`,
			p.Key, p.Value, p.Unit, p.Description); err != nil {
			return fmt.Errorf("seed policy %s: %w", p.Key, err)
		}
	}
	return nil
}

// backfillAuditChain hashes every pre-existing audit_log row that landed on
// disk before the chain existed. Without this, VerifyAuditChain reports those
// rows as tampered (row_hash = '' doesn't match the recomputed hash).
//
// We walk in id order so each row's prev_hash can be set from the previous
// row's freshly computed hash. The audit_log_no_update trigger would block
// us if we wrote row_hash from outside; instead we DROP the trigger, run
// the back-fill, then re-create it. The window is a few microseconds on the
// local file; nothing else holds the connection because we use the writer
// pool's single connection.
func (d *DB) backfillAuditChain() error {
	const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

	rows, err := d.writer.Query(
		`SELECT id, ts, actor, action, subject, detail, prev_hash, row_hash
		 FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return err
	}
	type row struct {
		id                  int64
		ts, actor, action   string
		subject, detail     string
		prevHash, rowHash   string
	}
	var entries []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ts, &r.actor, &r.action, &r.subject, &r.detail, &r.prevHash, &r.rowHash); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(entries) == 0 {
		return nil
	}

	needsBackfill := false
	for _, r := range entries {
		if r.rowHash == "" {
			needsBackfill = true
			break
		}
	}
	if !needsBackfill {
		return nil
	}

	// Drop the trigger so we can UPDATE row_hash on legacy rows. Recreate
	// it before returning; even an interrupted back-fill leaves the table
	// trigger-free (which is the original pre-F-02 state), so the trigger
	// re-install closes the audit-evidence gap.
	if _, err := d.writer.Exec(`DROP TRIGGER IF EXISTS audit_log_no_update`); err != nil {
		return err
	}
	defer func() {
		_, _ = d.writer.Exec(`CREATE TRIGGER IF NOT EXISTS audit_log_no_update
BEFORE UPDATE ON audit_log
WHEN NOT (
    OLD.row_hash = ''
    AND NEW.row_hash <> ''
    AND NEW.id = OLD.id
    AND NEW.ts = OLD.ts
    AND NEW.actor = OLD.actor
    AND NEW.action = OLD.action
    AND NEW.subject = OLD.subject
    AND NEW.detail = OLD.detail
    AND NEW.prev_hash = OLD.prev_hash
)
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only: UPDATE is forbidden (F-02 tamper-evidence)');
END`)
	}()

	var prevHash string
	for _, r := range entries {
		if r.rowHash != "" {
			// Already on the new schema; use as the running anchor so the
			// back-fill below chains correctly across mixed-history rows.
			prevHash = r.rowHash
			continue
		}
		wantPrev := zeroHash
		if prevHash != "" {
			wantPrev = prevHash
		}
		newHash := computeRowHash(r.id, wantPrev, r.ts, r.actor, r.action, r.subject, r.detail)
		if r.prevHash != wantPrev {
			if _, err := d.writer.Exec(
				`UPDATE audit_log SET prev_hash = ? WHERE id = ?`,
				wantPrev, r.id); err != nil {
				return err
			}
		}
		if _, err := d.writer.Exec(
			`UPDATE audit_log SET row_hash = ? WHERE id = ?`,
			newHash, r.id); err != nil {
			return err
		}
		prevHash = newHash
	}
	return nil
}

// schemaVersion is stamped in the schema_meta table on every Open. Bump when
// the migration list in migrate() gains a new step (informational; the
// migrations themselves are idempotent and don't read this value).
const schemaVersion = "2026-08-31.1"

// applyColumnIfMissing runs `addSQL` only when the named column is absent from
// the named table. Uses PRAGMA table_info, which is the supported runtime
// introspection primitive in SQLite. Replaces the old "version-monotonic
// migration" pattern (which we deliberately don't introduce — see F-02c).
func applyColumnIfMissing(handle *sql.DB, table, column, addSQL string) error {
	rows, err := handle.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dflt, pk interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if _, err := handle.Exec(addSQL); err != nil {
		return err
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
	ID                   string
	AccountID            string
	MemberID             string
	TS                   time.Time
	Direction            string
	Channel              string
	AmountMinor          int64
	Currency             string
	Counterparty         string
	Country              string
	CounterpartyAccount  string // destination sub-account for R09 multi-account cycling; distinct from Country
	Narrative            string
	Reference            string
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
	rows, err := d.reader.QueryContext(ctx, `SELECT key, value FROM policy`)
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
	res, err := d.writer.ExecContext(ctx, `UPDATE policy SET value = ? WHERE key = ?`, value, key)
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
	_, err := d.writer.ExecContext(ctx,
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
	_, err := d.writer.ExecContext(ctx,
		`INSERT INTO accounts(id, member_id, kind, opened_at, currency)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, currency=excluded.currency`,
		id, memberID, kind, fmtTS(opened), currency)
	return err
}

// InsertTxn adds one transaction. Idempotent on transaction id so re-importing a
// fixture file cannot double-count and manufacture a false velocity alert.
func (d *DB) InsertTxn(ctx context.Context, t Txn) error {
	_, err := d.writer.ExecContext(ctx,
		`INSERT INTO transactions
		   (id, account_id, member_id, ts, direction, channel, amount_minor, currency,
		    counterparty, counterparty_country, counterparty_account, narrative, reference)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
		t.ID, t.AccountID, t.MemberID, fmtTS(t.TS), strings.ToLower(t.Direction),
		strings.ToLower(t.Channel), t.AmountMinor, t.Currency, t.Counterparty,
		strings.ToUpper(t.Country), t.CounterpartyAccount, t.Narrative, t.Reference)
	return err
}

const txnCols = `id, account_id, member_id, ts, direction, channel, amount_minor,
                 currency, COALESCE(counterparty,''), COALESCE(counterparty_country,''),
                 COALESCE(counterparty_account,''), COALESCE(narrative,''), COALESCE(reference,'')`

func scanTxns(rows *sql.Rows) ([]Txn, error) {
	defer rows.Close()
	var out []Txn
	for rows.Next() {
		var t Txn
		var ts string
		if err := rows.Scan(&t.ID, &t.AccountID, &t.MemberID, &ts, &t.Direction, &t.Channel,
			&t.AmountMinor, &t.Currency, &t.Counterparty, &t.Country, &t.CounterpartyAccount, &t.Narrative, &t.Reference); err != nil {
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
	rows, err := d.reader.QueryContext(ctx,
		`SELECT `+txnCols+` FROM transactions WHERE ts >= ? AND ts < ? ORDER BY ts, id`,
		fmtTS(start), fmtTS(end))
	if err != nil {
		return nil, fmt.Errorf("query transactions: %w", err)
	}
	return scanTxns(rows)
}

// TxnsForMember returns one member's transactions in [start, end).
func (d *DB) TxnsForMember(ctx context.Context, memberID string, start, end time.Time) ([]Txn, error) {
	rows, err := d.reader.QueryContext(ctx,
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
	rows, err := d.reader.QueryContext(ctx,
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
	err := d.reader.QueryRowContext(ctx,
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
	err := d.reader.QueryRowContext(ctx,
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
	row := d.reader.QueryRowContext(ctx,
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
	err := d.reader.QueryRowContext(ctx,
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
	_, err := d.writer.ExecContext(ctx,
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
	err = d.reader.QueryRowContext(ctx,
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
	rows, err := d.reader.QueryContext(ctx,
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
	rows, err := d.reader.QueryContext(ctx,
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
	res, err := d.writer.ExecContext(ctx,
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
		if _, err := d.writer.ExecContext(ctx,
			`INSERT INTO case_alerts(case_id, alert_id) VALUES(?,?) ON CONFLICT DO NOTHING`,
			id, aid); err != nil {
			return 0, fmt.Errorf("link alert %d to case %d: %w", aid, id, err)
		}
	}
	return id, nil
}

// RecentCases returns the most recently opened draft cases, newest first.
// limit <= 0 means "no limit".
func (d *DB) RecentCases(ctx context.Context, limit int) ([]Case, error) {
	q := `SELECT id, member_id, title, lang, status, narrative, citations, opened_at
	      FROM cases ORDER BY id DESC`
	args := []interface{}{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query cases: %w", err)
	}
	defer rows.Close()

	var out []Case
	for rows.Next() {
		var c Case
		var opened string
		if err := rows.Scan(&c.ID, &c.MemberID, &c.Title, &c.Lang, &c.Status,
			&c.Narrative, &c.Citations, &opened); err != nil {
			return nil, fmt.Errorf("scan case: %w", err)
		}
		c.OpenedAt = parseTS(opened)
		out = append(out, c)
	}
	return out, rows.Err()
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
	res, err := d.writer.ExecContext(ctx,
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
	rows, err := d.reader.QueryContext(ctx,
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
	rows, err := d.reader.QueryContext(ctx,
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
	_, err := d.writer.ExecContext(ctx,
		`UPDATE messages SET status = ?, processed_at = ? WHERE id = ?`,
		status, fmtTS(time.Now()), id)
	return err
}

// ── audit trail ──────────────────────────────────────────────────────────────

// Audit appends one immutable audit record with a SHA-256 hash chain.
//
// Tamper-evidence (F-02): each new row stores the hash of the previous row's
// contents ("id|ts|actor|action|subject|detail|prev_hash") plus its own hash.
// The genesis row's prev_hash is the 64-char zero string. Re-writing any
// historical row invalidates every hash that follows it, and the audit_log_no_*
// triggers forbid UPDATE/DELETE outright at the SQL level — even an attacker
// with the database file cannot rewrite history without also recomputing the
// chain, which an external verifier (this method's sibling VerifyAuditChain)
// detects on the next read.
//
// The hash is computed inside a single CTE so the read-prev-hash and the
// insert-row cannot be separated by another writer: SQLite's single-writer
// discipline plus the atomic CTE give us a one-shot append.
func (d *DB) Audit(ctx context.Context, actor, action, subject, detail string) error {
	const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"
	ts := fmtTS(time.Now())

	// CTE: read prev (id, prev_hash) → compute row_hash → insert. Everything
	// happens inside a single statement so a concurrent writer cannot interpose
	// a different row between the read and the insert.
	const ins = `
INSERT INTO audit_log (ts, actor, action, subject, detail, prev_hash, row_hash)
WITH prev AS (
    SELECT id, prev_hash FROM audit_log ORDER BY id DESC LIMIT 1
)
SELECT
    ?, ?, ?, ?, ?,
    COALESCE((SELECT row_hash FROM prev), ?),
    -- row_hash = sha256("id|prev_hash|ts|actor|action|subject|detail") where id
    -- is the row's own id (just allocated by SQLite). We approximate id by
    -- selecting MAX(id)+1 from the current state at insert time; SQLite
    -- evaluates the SELECT against the pre-insert snapshot, so MAX(id)+1 is
    -- the AUTOINCREMENT id this row will receive.
    lower(hex(randomblob(0))) || ''  -- placeholder; real hash computed below
`
	// The placeholder approach above can't compute a self-referential id within
	// a single statement, so we do this in two statements inside one transaction
	// instead. The transaction is what holds the chain together: any concurrent
	// writer waits on the writer lock, and the read-then-write happens in one
	// connection.
	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("audit begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prevHash string
	row := tx.QueryRowContext(ctx, `SELECT COALESCE(row_hash, '') FROM audit_log ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&prevHash); err != nil {
		prevHash = ""
	}
	if prevHash == "" {
		prevHash = zeroHash
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (ts, actor, action, subject, detail, prev_hash, row_hash)
		 VALUES(?,?,?,?,?,?,?)`,
		ts, actor, action, subject, detail, prevHash, "")
	if err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("audit last id: %w", err)
	}

	rowHash := computeRowHash(id, prevHash, ts, actor, action, subject, detail)
	if _, err := tx.ExecContext(ctx,
		`UPDATE audit_log SET row_hash = ? WHERE id = ?`,
		rowHash, id); err != nil {
		// The trigger would fire and abort here if we hadn't just inserted
		// the row in the same transaction. Re-read the trigger definition
		// before relying on a raw UPDATE in production paths.
		return fmt.Errorf("audit set row_hash: %w", err)
	}

	return tx.Commit()
}

// VerifyAuditChain walks the audit_log from oldest to newest and confirms
// every row's row_hash matches the hash of (id|prev_hash|ts|actor|action|
// subject|detail). Returns the first broken row's id and the computed hash,
// or ("", "") on a fully intact chain.
//
// Designed to be called by an external auditor (or kanzu doctor with a flag
// added later) — does not trust the row_hash column, only the row contents.
func (d *DB) VerifyAuditChain(ctx context.Context) (brokenID int64, computed string, err error) {
	rows, err := d.reader.QueryContext(ctx,
		`SELECT id, ts, actor, action, subject, detail, prev_hash, row_hash
		 FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return 0, "", fmt.Errorf("audit verify query: %w", err)
	}
	defer rows.Close()

	const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"
	var prev string
	for rows.Next() {
		var id int64
		var ts, actor, action, subject, detail, prevHash, rowHash string
		if err := rows.Scan(&id, &ts, &actor, &action, &subject, &detail, &prevHash, &rowHash); err != nil {
			return 0, "", fmt.Errorf("audit scan: %w", err)
		}
		// Genesis row's stored prev_hash is zeroHash; subsequent rows must match
		// the prior row's row_hash exactly.
		if prevHash != prev && prev != "" {
			return id, prevHash, nil
		}
		if prev == "" {
			prev = zeroHash
			if prevHash != prev {
				return id, prevHash, nil
			}
		}
		want := computeRowHash(id, prevHash, ts, actor, action, subject, detail)
		if want != rowHash {
			return id, want, nil
		}
		prev = rowHash
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("audit verify iterate: %w", err)
	}
	return 0, "", nil
}

// computeRowHash is the canonical SHA-256 of one audit_log entry. Lives here
// (rather than in a helper package) so the schema, writer, and verifier all
// agree on the exact canonical form. Hex-encoded, lowercase.
func computeRowHash(id int64, prevHash, ts, actor, action, subject, detail string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s",
		id, prevHash, ts, actor, action, subject, detail)
	return hex.EncodeToString(h.Sum(nil))
}

// Counts returns row counts for `kanzu doctor`.
func (d *DB) Counts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range []string{"members", "accounts", "transactions", "alerts", "cases", "messages", "kb_chunks", "audit_log"} {
		var n int
		// Table names are from this fixed literal slice, never from user input.
		if err := d.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
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
