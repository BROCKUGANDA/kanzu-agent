// Package i18n carries Kanzu Agent's bilingual (English / Kiswahili) surface.
//
// Why this package exists at all: the base model (Qwen2.5-1.5B-Instruct) has
// solid instruction-following but Kiswahili is not among its officially
// supported languages. Relying on its zero-shot Kiswahili generation for a
// compliance artefact would be irresponsible. So Kiswahili functionality is
// delivered structurally instead:
//
//  1. All operator-facing chrome, intents, tool names and report scaffolding are
//     translated deterministically here (no model involved).
//  2. Retrieval runs over a Kiswahili knowledge corpus, so Kiswahili regulatory
//     text reaches the prompt verbatim and the model paraphrases rather than
//     invents.
//  3. The lexicon below expands queries across languages, so a Kiswahili query
//     retrieves English source material and vice versa.
//
// The model therefore fills constrained slots inside a Kiswahili scaffold it did
// not have to construct. This is the load-bearing part of the african_alpha
// claim, and it degrades gracefully rather than hallucinating.
package i18n

import (
	"sort"
	"strings"
)

// Lang is a BCP-47 subset: "en" or "sw".
type Lang string

const (
	EN Lang = "en"
	SW Lang = "sw"
)

// Parse maps loose user input onto a supported language.
func Parse(v string) Lang {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "sw"), v == "kiswahili", v == "swahili":
		return SW
	default:
		return EN
	}
}

// Name returns the endonym, used in report headers.
func (l Lang) Name() string {
	if l == SW {
		return "Kiswahili"
	}
	return "English"
}

// catalog holds every operator-visible string. Keys are stable identifiers so a
// missing translation is a visible test failure rather than silent English.
var catalog = map[string]map[Lang]string{
	"app.tagline": {
		EN: "Offline compliance copilot for savings groups and micro-SMEs",
		SW: "Msaidizi wa uzingatiaji unaofanya kazi nje ya mtandao kwa vikoba na biashara ndogo",
	},
	"chat.banner": {
		EN: "Kanzu Agent — fully offline. Type :help for commands, :quit to exit.",
		SW: "Kanzu Agent — nje ya mtandao kabisa. Andika :help kwa amri, :quit kutoka.",
	},
	"chat.prompt": {EN: "you", SW: "wewe"},
	"chat.thinking": {
		EN: "working (deterministic checks first, then narration)…",
		SW: "inafanya kazi (ukaguzi wa uhakika kwanza, kisha maelezo)…",
	},
	"chat.help": {
		EN: "  :lang en|sw   switch language\n  :stats        show thermal + throughput stats\n  :plan         show the last execution plan\n  :quit         exit",
		SW: "  :lang en|sw   badilisha lugha\n  :stats        onyesha hali ya joto na kasi\n  :plan         onyesha mpango wa mwisho\n  :quit         toka",
	},
	"plan.header":     {EN: "EXECUTION PLAN", SW: "MPANGO WA UTEKELEZAJI"},
	"evidence.header": {EN: "DETERMINISTIC EVIDENCE", SW: "USHAHIDI WA UHAKIKA"},
	"report.header":   {EN: "SUSPICIOUS ACTIVITY NOTE", SW: "TAARIFA YA SHUGHULI ZA KUTILIWA SHAKA"},
	"report.subject":  {EN: "Member", SW: "Mwanachama"},
	"report.period":   {EN: "Period", SW: "Kipindi"},
	"report.typology": {EN: "Typology", SW: "Aina ya hatari"},
	"report.findings": {EN: "Findings", SW: "Matokeo"},
	"report.citations": {
		EN: "Sources consulted (local knowledge base)",
		SW: "Vyanzo vilivyotumika (hifadhi ya maarifa ya ndani)",
	},
	"report.disclaimer": {
		EN: "Generated on-device by Kanzu Agent. Findings are produced by deterministic rules; the narrative is model-assisted. A human compliance officer must review and sign before any regulatory filing. No accusation of criminal conduct is implied.",
		SW: "Imetayarishwa kwenye kifaa hiki na Kanzu Agent. Matokeo yanatokana na kanuni za uhakika; maelezo yameandikwa kwa msaada wa modeli. Afisa wa uzingatiaji lazima aipitie na kuisaini kabla ya kuwasilishwa kwa mdhibiti. Hakuna tuhuma ya uhalifu inayodokezwa.",
	},
	"alerts.none": {
		EN: "No rule triggered for the requested window. Nothing to escalate.",
		SW: "Hakuna kanuni iliyochochewa katika kipindi kilichoombwa. Hakuna la kupandisha.",
	},
	"alerts.count":  {EN: "alerts raised", SW: "ilani zilizotolewa"},
	"severity.high": {EN: "high", SW: "kubwa"},
	"severity.med":  {EN: "medium", SW: "wastani"},
	"severity.low":  {EN: "low", SW: "ndogo"},
	"inbox.empty": {
		EN: "Queue empty. Messages accepted offline are processed here in batches.",
		SW: "Foleni ni tupu. Ujumbe uliopokewa nje ya mtandao unashughulikiwa hapa kwa makundi.",
	},
	"inbox.queued":    {EN: "queued for delivery when a link is available", SW: "imepangwa kutumwa mtandao utakapopatikana"},
	"model.offline":   {EN: "model weights missing — deterministic findings only", SW: "modeli haipo — matokeo ya kanuni pekee"},
	"thermal.cooling": {EN: "thermal guard: pausing to cool", SW: "ulinzi wa joto: inasimama ipoe"},
}

// T looks up a catalog key, falling back to English then to the key itself.
// A missing key surfaces as the key, which is loud enough to catch in review.
func T(l Lang, key string) string {
	if byLang, ok := catalog[key]; ok {
		if s, ok := byLang[l]; ok && s != "" {
			return s
		}
		if s, ok := byLang[EN]; ok {
			return s
		}
	}
	return key
}

// lexicon maps a concept to its surface forms in both languages. It powers two
// things: cross-language retrieval expansion, and intent detection.
//
// Terms are lowercase and diacritic-free to match the FTS5 tokenizer
// configuration (unicode61 remove_diacritics 2).
var lexicon = map[string][]string{
	"deposit":     {"deposit", "deposits", "amana", "kuweka", "weka"},
	"withdrawal":  {"withdrawal", "withdraw", "kutoa", "uchukuzi", "kuchomoa"},
	"transfer":    {"transfer", "remittance", "uhamisho", "kutuma", "hawala"},
	"cash":        {"cash", "currency", "fedha", "pesa", "taslimu"},
	"loan":        {"loan", "credit", "mkopo", "mikopo"},
	"savings":     {"savings", "share", "akiba", "hisa"},
	"member":      {"member", "customer", "mwanachama", "wanachama", "mteja"},
	"account":     {"account", "akaunti", "hesabu"},
	"suspicious":  {"suspicious", "unusual", "kutiliwa", "shaka", "isiyo", "kawaida", "tuhuma"},
	"structuring": {"structuring", "smurfing", "kugawanya", "mgawanyo", "kuvunja"},
	"threshold":   {"threshold", "limit", "kiwango", "kikomo", "ukomo"},
	"reporting":   {"reporting", "report", "kuripoti", "taarifa", "ripoti"},
	"laundering":  {"laundering", "utakatishaji", "kusafisha"},
	"terrorism":   {"terrorism", "financing", "ugaidi", "ufadhili"},
	"kyc":         {"kyc", "identity", "identification", "utambulisho", "uthibitisho"},
	"risk":        {"risk", "hatari", "athari"},
	"dormant":     {"dormant", "inactive", "tulivu", "isiyotumika"},
	"velocity":    {"velocity", "frequency", "kasi", "mara", "mfululizo"},
	"crossborder": {"cross-border", "foreign", "nje", "kigeni", "mpakani"},
	"pep":         {"pep", "politically", "exposed", "kisiasa", "mwanasiasa"},
	"sacco":       {"sacco", "cooperative", "chama", "ushirika", "kikoba", "vikoba"},
	"mobilemoney": {"mpesa", "m-pesa", "mobile", "wallet", "simu", "kapu"},
	"compliance":  {"compliance", "uzingatiaji", "ufuatiliaji"},
	"audit":       {"audit", "ukaguzi", "hesabu"},
	"committee":   {"committee", "board", "kamati", "bodi"},
	"week":        {"week", "weekly", "wiki"},
	"month":       {"month", "monthly", "mwezi"},
	"today":       {"today", "leo"},
	"draft":       {"draft", "write", "andika", "tayarisha", "andaa"},
	"explain":     {"explain", "why", "eleza", "kwanini", "nini"},
	"scan":        {"scan", "check", "flag", "chunguza", "kagua", "angalia", "onyesha"},
	"summary":     {"summary", "overview", "muhtasari", "jumla"},
}

// reverse index built once at init: surface form -> concept.
var conceptOf = func() map[string]string {
	m := make(map[string]string, 256)
	for concept, forms := range lexicon {
		for _, f := range forms {
			m[f] = concept
		}
	}
	return m
}()

// ConceptsIn returns the distinct domain concepts present in free text, in
// deterministic order. Used by the planner for intent detection and by the RAG
// layer for query expansion.
func ConceptsIn(text string) []string {
	seen := map[string]bool{}
	for _, tok := range Tokenize(text) {
		if c, ok := conceptOf[tok]; ok {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Expand returns the union of every surface form for the concepts detected in
// text, plus the original tokens. This is what makes a Kiswahili question
// retrieve English regulatory text.
func Expand(text string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 32)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, tok := range Tokenize(text) {
		add(tok)
	}
	for _, c := range ConceptsIn(text) {
		for _, form := range lexicon[c] {
			add(form)
		}
	}
	sort.Strings(out)
	return out
}

// stopwords are dropped before retrieval. Both languages, kept small on purpose:
// over-aggressive stopping hurts short compliance queries.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "for": true, "and": true,
	"to": true, "in": true, "on": true, "is": true, "are": true, "was": true,
	"were": true, "this": true, "that": true, "with": true, "from": true,
	"me": true, "my": true, "please": true, "all": true, "any": true, "it": true,
	"na": true, "ya": true, "wa": true, "za": true, "la": true, "kwa": true,
	"ni": true, "katika": true, "hii": true, "hiyo": true, "au": true,
	"tafadhali": true, "yangu": true, "zote": true, "kama": true,
}

// Tokenize lowercases, splits on non-alphanumerics, and drops stopwords and
// 1-character fragments. Kept in this package so the planner, the RAG layer and
// the rule engine all agree on what a token is.
func Tokenize(text string) []string {
	lowered := strings.ToLower(text)
	fields := strings.FieldsFunc(lowered, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// DetectLang guesses the language of a request from lexical evidence. It biases
// toward English on a tie because English is the fallback for every catalog key.
func DetectLang(text string) Lang {
	toks := Tokenize(text)
	var sw, en int
	for _, t := range toks {
		if swMarkers[t] {
			sw++
		}
		if enMarkers[t] {
			en++
		}
	}
	if sw > en {
		return SW
	}
	return EN
}

var swMarkers = buildMarkers([]string{
	"amana", "mwanachama", "wanachama", "miamala", "muamala", "kagua", "chunguza",
	"eleza", "andika", "taarifa", "ripoti", "kiwango", "hatari", "shaka", "fedha",
	"pesa", "mkopo", "akiba", "wiki", "mwezi", "leo", "kamati", "muhtasari",
	"kugawanya", "utakatishaji", "uzingatiaji", "tulivu", "nje", "kigeni",
	"tafadhali", "onyesha", "nini", "kwanini", "sacco", "kikoba", "vikoba",
})

var enMarkers = buildMarkers([]string{
	"flag", "suspicious", "transactions", "transaction", "member", "draft",
	"report", "compliance", "explain", "why", "threshold", "deposit", "deposits",
	"week", "month", "today", "committee", "summary", "structuring", "laundering",
	"dormant", "cross", "border", "check", "scan", "show", "note", "risk",
})

func buildMarkers(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
