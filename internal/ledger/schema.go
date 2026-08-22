package ledger

// Schema is the complete DDL for Kanzu Agent's local store.
//
// Design rules encoded here:
//
//   - Money is stored as integer minor units (cents). Float money in an AML
//     ledger produces reconciliation drift that looks exactly like the rounding
//     patterns a structuring rule is trying to detect.
//   - Timestamps are RFC3339 UTC text. SQLite has no native date type, and text
//     dates sort lexicographically, which is what the window queries rely on.
//   - Numeric policy thresholds live in the `policy` table, not in Go constants
//     and not in the knowledge base. Reporting thresholds are jurisdiction- and
//     institution-specific and they change by gazette notice; hard-coding a
//     figure into a compliance tool is how you ship confidently wrong advice.
//     Each SACCO sets its own, and every alert records the threshold it fired
//     against so an old alert stays interpretable after a policy change.
//   - `audit_log` is append-only by convention and is written for every state
//     change, including read-only scans. Supervisory audits ask who ran what.
const Schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS members (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    joined_at       TEXT NOT NULL,
    kyc_level       INTEGER NOT NULL DEFAULT 0,   -- 0 none, 1 basic, 2 verified, 3 enhanced
    kyc_verified_at TEXT,
    risk_band       TEXT NOT NULL DEFAULT 'low',  -- low | medium | high
    is_pep          INTEGER NOT NULL DEFAULT 0,
    dormant_since   TEXT,
    home_branch     TEXT,
    id_type         TEXT,
    id_ref          TEXT,                         -- last 4 only; never the full document number
    notes           TEXT
);

CREATE TABLE IF NOT EXISTS accounts (
    id            TEXT PRIMARY KEY,
    member_id     TEXT NOT NULL REFERENCES members(id),
    kind          TEXT NOT NULL,                  -- savings | share | loan | wallet
    opened_at     TEXT NOT NULL,
    currency      TEXT NOT NULL DEFAULT 'UGX',
    balance_minor INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_accounts_member ON accounts(member_id);

CREATE TABLE IF NOT EXISTS transactions (
    id                  TEXT PRIMARY KEY,
    account_id          TEXT NOT NULL REFERENCES accounts(id),
    member_id           TEXT NOT NULL REFERENCES members(id),
    ts                  TEXT NOT NULL,            -- RFC3339 UTC
    direction           TEXT NOT NULL,            -- credit | debit
    channel             TEXT NOT NULL,            -- cash | mobile | transfer | cheque | agent
    amount_minor        INTEGER NOT NULL,
    currency            TEXT NOT NULL DEFAULT 'UGX',
    counterparty        TEXT,
    counterparty_country TEXT,                    -- ISO-3166 alpha-2
    narrative           TEXT,
    reference           TEXT
);
CREATE INDEX IF NOT EXISTS idx_txn_ts        ON transactions(ts);
CREATE INDEX IF NOT EXISTS idx_txn_member_ts ON transactions(member_id, ts);

CREATE TABLE IF NOT EXISTS alerts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id        TEXT NOT NULL,
    member_id      TEXT NOT NULL REFERENCES members(id),
    window_start   TEXT NOT NULL,
    window_end     TEXT NOT NULL,
    severity       TEXT NOT NULL,                 -- low | medium | high
    score          REAL NOT NULL,
    amount_minor   INTEGER NOT NULL DEFAULT 0,
    txn_count      INTEGER NOT NULL DEFAULT 0,
    threshold_used INTEGER NOT NULL DEFAULT 0,    -- the policy value in force at fire time
    txn_ids        TEXT NOT NULL DEFAULT '',      -- comma-separated evidence trail
    rationale      TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'open',  -- open | escalated | dismissed
    created_at     TEXT NOT NULL,
    UNIQUE(rule_id, member_id, window_start, window_end)
);
CREATE INDEX IF NOT EXISTS idx_alerts_member ON alerts(member_id);
CREATE INDEX IF NOT EXISTS idx_alerts_created ON alerts(created_at);

CREATE TABLE IF NOT EXISTS cases (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id  TEXT NOT NULL REFERENCES members(id),
    title      TEXT NOT NULL,
    lang       TEXT NOT NULL DEFAULT 'en',
    status     TEXT NOT NULL DEFAULT 'draft',     -- draft | reviewed | filed
    narrative  TEXT NOT NULL DEFAULT '',
    citations  TEXT NOT NULL DEFAULT '',
    opened_at  TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS case_alerts (
    case_id  INTEGER NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
    alert_id INTEGER NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    PRIMARY KEY (case_id, alert_id)
);

-- Store-and-forward queue. This is the "decentralised messaging interface":
-- field officers on feature phones or intermittent links drop requests in, the
-- agent processes them in paced batches, and replies wait here until a link
-- exists. Nothing in Kanzu ever performs the delivery; an external syncing tool
-- drains outbound rows. That boundary is what keeps the agent itself offline.
CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    direction    TEXT NOT NULL,                   -- inbound | outbound
    channel      TEXT NOT NULL DEFAULT 'queue',   -- queue | sms | ussd | whatsapp
    peer         TEXT NOT NULL DEFAULT '',        -- officer or member reference
    lang         TEXT NOT NULL DEFAULT 'en',
    body         TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending', -- pending | processed | queued | failed
    reply_to     INTEGER REFERENCES messages(id),
    created_at   TEXT NOT NULL,
    processed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(direction, status, id);

CREATE TABLE IF NOT EXISTS policy (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    unit        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      TEXT NOT NULL,
    actor   TEXT NOT NULL,
    action  TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    detail  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);

-- Retrieval corpus. FTS5 keeps the inverted index on disk and only materialises
-- the rows a query actually matches, so a growing knowledge base costs disk
-- rather than resident memory. bm25() is SQLite's built-in ranking function.
-- unicode61 with remove_diacritics=2 tokenises Kiswahili correctly.
CREATE VIRTUAL TABLE IF NOT EXISTS kb_chunks USING fts5(
    title,
    body,
    source   UNINDEXED,
    lang     UNINDEXED,
    citation UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE IF NOT EXISTS kb_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// defaultPolicy seeds institution-settable thresholds.
//
// These are deliberately framed as the SACCO's *internal* control thresholds,
// not as statutory figures. The knowledge base explains the statutory obligation
// qualitatively and names the instrument; the number a rule fires against is
// always the institution's own, recorded on the alert.
var defaultPolicy = []struct {
	Key, Value, Unit, Description string
}{
	{"currency", "UGX", "", "Reporting currency for this institution."},
	{"internal_report_threshold_minor", "2800000000", "minor units",
		"Internal single-transaction escalation threshold (UGX 28,000,000). Set by the SACCO board; not a statutory figure."},
	{"structuring_window_hours", "72", "hours",
		"Look-back window for aggregating deposits when testing for structuring."},
	{"structuring_min_txns", "3", "count",
		"Minimum number of same-direction transactions before structuring can fire."},
	{"structuring_band_pct", "85", "percent",
		"A transaction is 'threshold-hugging' at or above this percentage of the internal threshold."},
	{"velocity_window_hours", "168", "hours",
		"Look-back window for transaction-velocity baselining (7 days)."},
	{"velocity_multiple", "4", "multiple",
		"Alert when a member's window volume exceeds this multiple of their trailing baseline."},
	{"velocity_min_baseline_txns", "4", "count",
		"Minimum historical transactions required before a velocity baseline is trusted."},
	{"round_amount_min_repeats", "3", "count",
		"Repeats of an identical round figure before the round-amount rule fires."},
	{"round_amount_modulus_minor", "1000000", "minor units",
		"An amount is 'round' when divisible by this (UGX 10,000)."},
	{"dormancy_days", "180", "days",
		"Days of inactivity after which an account is treated as dormant."},
	{"dormant_reactivation_pct", "50", "percent",
		"Reactivation alerts when post-dormancy value reaches this percentage of the internal threshold."},
	{"passthrough_window_hours", "48", "hours",
		"Window for detecting funds arriving and leaving again (layering)."},
	{"passthrough_ratio_pct", "80", "percent",
		"Outflow as a percentage of recent inflow that constitutes a pass-through."},
	{"kyc_tier1_limit_minor", "560000000", "minor units",
		"Cumulative value a basic-KYC member may transact per velocity window (UGX 5,600,000)."},
	{"high_risk_countries", "IR,KP,MM,SS,SY,YE,AF,LY",
		"ISO-3166 alpha-2",
		"Institution watchlist for enhanced due diligence. Review against the current FATF public statements each quarter."},
	{"pep_review_pct", "25", "percent",
		"PEP-linked flows above this percentage of the internal threshold require documented review."},
}
