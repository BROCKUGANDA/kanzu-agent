// Package rag is Kanzu Agent's retrieval layer over the local compliance corpus.
//
// There is no embedding model here, and that is a deliberate choice rather than a
// shortcut.
//
// The obvious design would load a small sentence encoder (MiniLM, bge-small) and
// do dense retrieval. On the target hardware that costs 100-400 MB of resident
// memory that has to coexist with a 1.0 GB GGUF, plus a second model to ship,
// version, and quantise. In exchange it buys paraphrase robustness over a corpus
// of roughly two hundred chunks of regulatory text.
//
// SQLite's FTS5 with BM25 ranking gets most of that benefit for none of the
// memory. The inverted index lives on disk; a query materialises only the rows it
// matches, so corpus growth costs disk rather than RAM. Ranking is deterministic
// and explainable, which matters more than usual here: when a compliance officer
// asks why a particular regulation was cited, "BM25 over these query terms" is an
// answer, and "cosine similarity in a 384-dimensional space" is not.
//
// The known weakness of lexical retrieval is vocabulary mismatch, and this domain
// has a severe case of it: a Kiswahili query will not lexically match English
// source text at all. That is handled directly in i18n.Expand, which maps both
// languages onto shared concepts and expands a query into every surface form. A
// query for "kugawanya miamala" retrieves the English structuring material, which
// is what a dense encoder would have been for.
package rag

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kanzu-agent/kanzu/internal/i18n"
)

// Chunk is one retrievable passage.
type Chunk struct {
	Title    string
	Body     string
	Source   string
	Lang     string
	Citation string
	Score    float64
}

// Index provides ingest and retrieval over kb_chunks.
type Index struct {
	db *sql.DB
}

// New wraps an open ledger handle. The corpus shares the ledger database so the
// agent opens exactly one file.
func New(db *sql.DB) *Index { return &Index{db: db} }

// document is a parsed knowledge-base file.
type document struct {
	Source   string
	Lang     string
	Citation string
	Chunks   []Chunk
}

// Ingest rebuilds the corpus from the knowledge directory.
//
// Full rebuild rather than incremental: the corpus is small, and a stale chunk in
// a compliance knowledge base is worse than a slow reindex.
func (ix *Index) Ingest(ctx context.Context, knowledgeDir string) (int, error) {
	var files []string
	err := filepath.WalkDir(knowledgeDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk %s: %w", knowledgeDir, err)
	}
	// Deterministic order so identical corpora produce identical rowids, which
	// keeps BM25 tie-breaking stable between machines.
	sort.Strings(files)

	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_chunks`); err != nil {
		return 0, fmt.Errorf("clear corpus: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO kb_chunks(title, body, source, lang, citation) VALUES(?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	total := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", path, err)
		}
		doc := parseDocument(string(raw), path)
		for _, c := range doc.Chunks {
			if _, err := stmt.ExecContext(ctx, c.Title, c.Body, c.Source, c.Lang, c.Citation); err != nil {
				return 0, fmt.Errorf("insert chunk %q from %s: %w", c.Title, path, err)
			}
			total++
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kb_meta(key, value) VALUES('chunks', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprint(total)); err != nil {
		return 0, err
	}
	// Merge the FTS index into one b-tree segment. Without this the index stays
	// fragmented across per-insert segments and every query touches all of them.
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_chunks(kb_chunks) VALUES('optimize')`); err != nil {
		return 0, fmt.Errorf("optimize corpus: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

// parseDocument splits a markdown file into chunks on level-2 headings.
//
// Front matter is an optional leading `---` block carrying lang, source and
// citation. Citation is per-document because regulatory text is cited at the
// instrument level, not the paragraph level.
func parseDocument(text, path string) document {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	doc := document{
		Source: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Lang:   string(i18n.EN),
	}
	// Infer language from the containing directory (knowledge/sw/...) before
	// front matter, which may override it.
	if parent := filepath.Base(filepath.Dir(path)); parent == "sw" || parent == "en" {
		doc.Lang = parent
	}

	body := text
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			front := text[4 : 4+end]
			body = strings.TrimPrefix(text[4+end+4:], "\n")
			for _, line := range strings.Split(front, "\n") {
				key, val, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				key = strings.ToLower(strings.TrimSpace(key))
				val = strings.TrimSpace(val)
				switch key {
				case "lang":
					doc.Lang = string(i18n.Parse(val))
				case "source":
					doc.Source = val
				case "citation":
					doc.Citation = val
				}
			}
		}
	}

	docTitle := doc.Source
	var current Chunk
	var buf []string

	flush := func() {
		if current.Title == "" && len(buf) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		if text == "" {
			return
		}
		current.Body = text
		current.Source = doc.Source
		current.Lang = doc.Lang
		current.Citation = doc.Citation
		if current.Title == "" {
			current.Title = docTitle
		}
		doc.Chunks = append(doc.Chunks, current)
		current = Chunk{}
		buf = nil
	}

	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "# "):
			flush()
			docTitle = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case strings.HasPrefix(line, "## "):
			flush()
			current.Title = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		default:
			buf = append(buf, line)
		}
	}
	flush()
	return doc
}

// Retrieve returns the top-k passages for a query, preferring the operator's
// language but never excluding the other one.
//
// Two-stage: FTS5 does the heavy lifting on disk and returns a candidate pool,
// then Go re-ranks with a same-language bonus. Fetching 4k candidates rather
// than the whole corpus is what keeps this lazy.
func (ix *Index) Retrieve(ctx context.Context, query string, lang i18n.Lang, k int) ([]Chunk, error) {
	if k <= 0 {
		k = 5
	}
	terms := i18n.Expand(query)
	if len(terms) == 0 {
		return nil, nil
	}

	// Try the full expansion, then progressively narrower ones. A very broad OR
	// can rank a marginal passage above a precise one, and a very narrow query
	// can miss entirely; walking down the list gets the best available match.
	attempts := [][]string{terms}
	if core := i18n.Tokenize(query); len(core) > 0 {
		attempts = append(attempts, core)
	}

	for _, attempt := range attempts {
		match := buildMatch(attempt)
		if match == "" {
			continue
		}
		out, err := ix.search(ctx, match, lang, k)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, nil
}

func (ix *Index) search(ctx context.Context, match string, lang i18n.Lang, k int) ([]Chunk, error) {
	const pool = 4
	rows, err := ix.db.QueryContext(ctx,
		`SELECT title, body, source, lang, citation,
		        bm25(kb_chunks, 3.0, 1.0, 0.0, 0.0, 0.0) AS score
		 FROM kb_chunks
		 WHERE kb_chunks MATCH ?
		 ORDER BY score
		 LIMIT ?`,
		match, k*pool)
	if err != nil {
		// A malformed MATCH expression is a bug in buildMatch, not user error;
		// surface it rather than silently returning nothing.
		return nil, fmt.Errorf("corpus search (match=%q): %w", match, err)
	}
	defer rows.Close()

	var cands []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Title, &c.Body, &c.Source, &c.Lang, &c.Citation, &c.Score); err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// bm25() returns negative numbers, better matches being more negative.
	// Applying a multiplicative bonus to a negative score makes a same-language
	// hit more negative, i.e. better, which is the intent.
	const sameLangBonus = 1.25
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := cands[i].Score, cands[j].Score
		if cands[i].Lang == string(lang) {
			si *= sameLangBonus
		}
		if cands[j].Lang == string(lang) {
			sj *= sameLangBonus
		}
		if si != sj {
			return si < sj
		}
		return cands[i].Title < cands[j].Title
	})

	if len(cands) > k {
		cands = cands[:k]
	}
	return cands, nil
}

// buildMatch renders an FTS5 MATCH expression.
//
// Every term is double-quoted, which makes it a literal string in FTS5 grammar
// and neutralises the operator characters. i18n.Tokenize has already stripped
// everything outside [a-z0-9], so no term can contain a quote to escape; the
// quoting is belt-and-braces against a future tokenizer change.
func buildMatch(terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	// Cap the expansion. FTS5 handles long disjunctions fine, but past a few
	// dozen terms every document matches and BM25 stops discriminating.
	const maxTerms = 40
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.ReplaceAll(t, `"`, "")
		if t == "" {
			continue
		}
		if len(t) >= 5 {
			// Prefix match absorbs Kiswahili agglutination and English plurals:
			// "amana" also hits "amanazo", "deposit" also hits "deposits".
			parts = append(parts, `"`+t+`"*`)
		} else {
			parts = append(parts, `"`+t+`"`)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " OR ")
}

// Stats reports corpus composition, used by `kanzu doctor`.
func (ix *Index) Stats(ctx context.Context) (map[string]int, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT lang, COUNT(*) FROM kb_chunks GROUP BY lang ORDER BY lang`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			return nil, err
		}
		out[lang] = n
	}
	return out, rows.Err()
}

// Citations returns the distinct, ordered citation lines for a chunk set, for the
// "sources consulted" block of a report.
func Citations(chunks []Chunk) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range chunks {
		cit := strings.TrimSpace(c.Citation)
		if cit == "" {
			cit = c.Source
		}
		if cit == "" || seen[cit] {
			continue
		}
		seen[cit] = true
		out = append(out, cit)
	}
	return out
}

// Snippet trims a chunk body to at most n characters on a word boundary, so a
// long passage cannot crowd the deterministic evidence out of a 2048-token
// context window.
func Snippet(body string, n int) string {
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= n {
		return body
	}
	cut := body[:n]
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
