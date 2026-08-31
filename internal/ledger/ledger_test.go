package ledger

import (
	"context"
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