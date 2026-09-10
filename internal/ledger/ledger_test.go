package ledger

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditChainForward confirms the chain links correctly across multiple
// inserts, the genesis row's prev_hash is the 64-char zero string, and every
// subsequent row's prev_hash matches the previous row's row_hash.
func TestAuditChainForward(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := db.Audit(ctx, "test", "step", "subject", "detail"); err != nil {
			t.Fatalf("audit %d: %v", i, err)
		}
	}

	rows, err := db.SQL().QueryContext(ctx,
		`SELECT id, prev_hash, row_hash FROM audit_log ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"
	var (
		ids       []int64
		prevHash  []string
		rowHashes []string
	)
	for rows.Next() {
		var id int64
		var ph, rh string
		if err := rows.Scan(&id, &ph, &rh); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
		prevHash = append(prevHash, ph)
		rowHashes = append(rowHashes, rh)
	}
	if len(ids) != 5 {
		t.Fatalf("got %d rows, want 5", len(ids))
	}
	if prevHash[0] != zeroHash {
		t.Errorf("genesis prev_hash = %q, want zeroHash", prevHash[0])
	}
	for i := 1; i < len(ids); i++ {
		if prevHash[i] != rowHashes[i-1] {
			t.Errorf("row %d prev_hash = %q, want previous row_hash %q",
				ids[i], prevHash[i], rowHashes[i-1])
		}
	}
}

// TestVerifyAuditChainIntact confirms the verifier accepts a freshly-written
// chain and rejects nothing.
func TestVerifyAuditChainIntact(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := db.Audit(ctx, "test", "act", "subj", "det"); err != nil {
			t.Fatalf("audit %d: %v", i, err)
		}
	}
	id, computed, err := db.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id != 0 || computed != "" {
		t.Errorf("verify reported break at id=%d computed=%q, want (0,\"\")", id, computed)
	}
}

// TestVerifyAuditChainTampered confirms the verifier detects a tampered row.
// We bypass the trigger by writing a modified row_hash directly through a
// raw connection (simulating an attacker with file-system access).
func TestVerifyAuditChainTampered(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := db.Audit(ctx, "test", "act", "subj", "det"); err != nil {
			t.Fatalf("audit %d: %v", i, err)
		}
	}

	// Bypass the trigger by writing directly to the SQLite file. We delete the
	// triggers (which the writer also owns — we are simulating an attacker who
	// has the file but not the trigger definitions in their head), corrupt
	// row 1's detail column to flip its hash, then re-verify.
	if _, err := db.SQL().ExecContext(ctx, `DROP TRIGGER IF EXISTS audit_log_no_update`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `DROP TRIGGER IF EXISTS audit_log_no_delete`); err != nil {
		t.Fatalf("drop trigger 2: %v", err)
	}
	res, err := db.SQL().ExecContext(ctx,
		`UPDATE audit_log SET detail = 'TAMPERED' WHERE id = (SELECT id FROM audit_log ORDER BY id ASC LIMIT 1)`)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Fatal("tamper update affected 0 rows")
	}

	id, _, err := db.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id == 0 {
		t.Error("expected verifier to detect tampering; got id=0 (clean)")
	}
}

// TestAuditTriggersBlockUpdate confirms the F-02 trigger still rejects
// detail-altering UPDATEs after the chain is in place.
func TestAuditTriggersBlockUpdate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	if err := db.Audit(ctx, "test", "act", "subj", "det"); err != nil {
		t.Fatalf("audit: %v", err)
	}

	_, err = db.SQL().ExecContext(ctx, `UPDATE audit_log SET detail = 'changed'`)
	if err == nil {
		t.Fatal("expected UPDATE to be blocked by trigger; got nil")
	}
	if !strings.Contains(err.Error(), "audit_log is append-only") {
		t.Errorf("UPDATE error = %q, want one mentioning 'audit_log is append-only'", err.Error())
	}

	_, err = db.SQL().ExecContext(ctx, `DELETE FROM audit_log`)
	if err == nil {
		t.Fatal("expected DELETE to be blocked by trigger; got nil")
	}
	if !strings.Contains(err.Error(), "audit_log is append-only") {
		t.Errorf("DELETE error = %q, want one mentioning 'audit_log is append-only'", err.Error())
	}
}

// TestReaderWriterConcurrency is the F-05 proof: a long-running writer
// transaction does NOT block concurrent readers. Pre-fix, the single-conn
// setup queued readers behind the writer for the full transaction; in WAL
// mode, reads should pass through immediately.
func TestReaderWriterConcurrency(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	if err := db.Audit(ctx, "test", "seed", "subj", "det"); err != nil {
		t.Fatalf("audit: %v", err)
	}

	// Hold a writer transaction open in a goroutine; reads against the reader
	// pool must still complete.
	writerHeld := make(chan struct{})
	go func() {
		tx, err := db.writer.BeginTx(ctx, nil)
		if err != nil {
			t.Errorf("writer begin: %v", err)
			close(writerHeld)
			return
		}
		// Insert one row inside the transaction but DON'T commit yet.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO audit_log(ts, actor, action, subject, detail, prev_hash, row_hash)
			 VALUES(?, 'in-flight', 'hold', '', '', '', '')`,
			"2026-08-31T00:00:00Z"); err != nil {
			t.Errorf("writer insert: %v", err)
			_ = tx.Rollback()
			close(writerHeld)
			return
		}
		close(writerHeld)
		// Sleep long enough that any serialising reader pool would time out
		// the test's context budget. 200ms is way more than a page-cache
		// hit should need.
		time.Sleep(200 * time.Millisecond)
		_ = tx.Rollback()
	}()

	<-writerHeld

	// Now race the reader. Use a generous timeout so a regression shows up
	// as a hard failure, not a flake.
	done := make(chan error, 1)
	go func() {
		_, err := db.Counts(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("read while writer holds tx failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read blocked for >2s while writer held a transaction — F-05 regression")
	}
}

// TestMigrationAddsMissingColumns is the F-02c / F-03e regression: an existing
// DB (pre-this-audit) opens successfully and gets the new audit_log and
// transactions columns back-filled on first open. The simulated old DB is
// produced by dropping the new columns from a freshly-migrated one.
func TestMigrationAddsMissingColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")

	// Step 1: open a fresh DB to apply the current schema.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	if err := db.Audit(context.Background(), "test", "warm", "", ""); err != nil {
		t.Fatalf("warm audit: %v", err)
	}
	db.Close()

	// Step 2: simulate the pre-audit schema (no prev_hash, no row_hash, no
	// counterparty_account) by recreating the audit_log + transactions tables
	// without the new columns. SQLite's `CREATE TABLE ... (new shape)` replaces
	// the table; we copy existing rows out and back in so the row count
	// survives.
	scratch, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("scratch open: %v", err)
	}
	if _, err := scratch.Exec(`
		CREATE TABLE audit_log_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			subject TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatalf("create audit_log_new: %v", err)
	}
	if _, err := scratch.Exec(`INSERT INTO audit_log_new SELECT id, ts, actor, action, subject, detail FROM audit_log`); err != nil {
		t.Fatalf("copy audit_log: %v", err)
	}
	if _, err := scratch.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}
	if _, err := scratch.Exec(`ALTER TABLE audit_log_new RENAME TO audit_log`); err != nil {
		t.Fatalf("rename audit_log_new: %v", err)
	}
	if _, err := scratch.Exec(`
		CREATE TABLE transactions_new (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			ts TEXT NOT NULL,
			direction TEXT NOT NULL,
			channel TEXT NOT NULL,
			amount_minor INTEGER NOT NULL,
			currency TEXT NOT NULL DEFAULT 'UGX',
			counterparty TEXT,
			counterparty_country TEXT,
			narrative TEXT,
			reference TEXT
		)`); err != nil {
		t.Fatalf("create transactions_new: %v", err)
	}
	if _, err := scratch.Exec(`INSERT INTO transactions_new SELECT id, account_id, member_id, ts, direction, channel, amount_minor, currency, counterparty, counterparty_country, narrative, reference FROM transactions`); err != nil {
		t.Fatalf("copy transactions: %v", err)
	}
	if _, err := scratch.Exec(`DROP TABLE transactions`); err != nil {
		t.Fatalf("drop transactions: %v", err)
	}
	if _, err := scratch.Exec(`ALTER TABLE transactions_new RENAME TO transactions`); err != nil {
		t.Fatalf("rename transactions_new: %v", err)
	}
	scratch.Close()

	// Step 3: reopen. The migration should add the missing columns, the
	// existing audit row should back-fill prev_hash (zero hash) and row_hash
	// (empty), and Audit() should succeed.
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	if err := db.Audit(context.Background(), "test", "post-migration", "", ""); err != nil {
		t.Fatalf("post-migration audit: %v", err)
	}

	// Chain should verify intact across the back-filled row + the new row.
	if id, _, err := db.VerifyAuditChain(context.Background()); err != nil {
		t.Fatalf("verify chain: %v", err)
	} else if id != 0 {
		t.Errorf("post-migration chain broken at id=%d", id)
	}
}

// TestPolicySetValidation pins the production guards on `kanzu policy`:
// unknown keys, empty values, and non-integer numeric thresholds must fail
// closed rather than silently writing a value the engine will ignore.
func TestPolicySetValidation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	if err := db.Set(ctx, "test", "does_not_exist", "1"); err == nil {
		t.Error("unknown key accepted; want error")
	}
	if err := db.Set(ctx, "test", "structuring_min_txns", ""); err == nil {
		t.Error("empty value accepted; want error")
	}
	if err := db.Set(ctx, "test", "structuring_min_txns", "not-a-number"); err == nil {
		t.Error("non-integer value accepted for numeric policy; want error")
	}
	if err := db.Set(ctx, "test", "", "1"); err == nil {
		t.Error("empty key accepted; want error")
	}
	if err := db.Set(ctx, "test", "structuring_min_txns", "4"); err != nil {
		t.Errorf("valid numeric policy rejected: %v", err)
	}
	p, err := db.LoadPolicy(ctx)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if got := p.Int("structuring_min_txns", 0); got != 4 {
		t.Errorf("after set, structuring_min_txns = %d, want 4", got)
	}
	// Non-numeric keys (currency) still accept free-form values.
	if err := db.Set(ctx, "test", "currency", "UGX"); err != nil {
		t.Errorf("currency set rejected: %v", err)
	}
}

// TestSafeIdent pins the migration table-name guard.
func TestSafeIdent(t *testing.T) {
	for _, ok := range []string{"audit_log", "transactions", "policy", "t1", "_x"} {
		if !safeIdent(ok) {
			t.Errorf("safeIdent(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "a;b", "a b", "a-b", "policy; DROP TABLE", "a\"b"} {
		if safeIdent(bad) {
			t.Errorf("safeIdent(%q) = true, want false", bad)
		}
	}
}

// TestInsertTxnReanchorsOnUpsert pins the demo-date fix: re-running init with a
// new anchor must move fixture timestamps. DO NOTHING would freeze the original
// dates so "this week" is empty days later.
func TestInsertTxnReanchorsOnUpsert(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	txn := Txn{
		ID: "T-1", AccountID: "A-1", MemberID: "M-001", TS: t1,
		Direction: "credit", Channel: "cash", AmountMinor: 100000, Currency: "UGX",
	}
	if err := db.UpsertMember(ctx, Member{ID: "M-001", Name: "Test", JoinedAt: t1.AddDate(-1, 0, 0)}); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := db.UpsertAccount(ctx, "A-1", "M-001", "savings", "UGX", t1); err != nil {
		t.Fatalf("account: %v", err)
	}
	if err := db.InsertTxn(ctx, txn); err != nil {
		t.Fatalf("insert: %v", err)
	}

	t2 := t1.AddDate(0, 0, 30)
	txn.TS = t2
	if err := db.InsertTxn(ctx, txn); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.TxnsBetween(ctx, t2.Add(-time.Hour), t2.Add(time.Hour))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || !got[0].TS.Equal(t2) {
		t.Fatalf("re-anchor failed: got %d txns, ts=%v want %v", len(got), got, t2)
	}
	// Still exactly one row — no double-count.
	all, err := db.TxnsBetween(ctx, t1.AddDate(-1, 0, 0), t2.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 txn after upsert, got %d", len(all))
	}
}