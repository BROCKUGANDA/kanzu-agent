// Package i18n carries Kanzu Agent's trilingual (English / Kiswahili / Luganda)
// surface.
//
// Why this package exists at all: the base model (Qwen2.5-1.5B-Instruct) has
// solid instruction-following but Kiswahili and Luganda are not among its
// officially supported languages. Relying on its zero-shot generation for a
// compliance artefact would be irresponsible. So non-English functionality is
// delivered structurally instead:
//
//  1. All operator-facing chrome, intents, tool names and report scaffolding are
//     translated deterministically here (no model involved).
//  2. Retrieval runs over language-specific knowledge corpora, so regulatory
//     text reaches the prompt verbatim and the model paraphrases rather than
//     invents.
//  3. The lexicon below expands queries across languages, so a Luganda query
//     retrieves English source material and vice versa.
//
// The model therefore fills constrained slots inside a language scaffold it did
// not have to construct. This is the load-bearing part of the african_alpha
// claim, and it degrades gracefully rather than hallucinating.
package i18n

import (
	"sort"
	"strings"
)

// Lang is a BCP-47 subset: "en", "sw", or "lg".
type Lang string

const (
	EN Lang = "en"
	SW Lang = "sw"
	LG Lang = "lg"
)

// Parse maps loose user input onto a supported language.
func Parse(v string) Lang {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "sw"), v == "kiswahili", v == "swahili":
		return SW
	case strings.HasPrefix(v, "lg"), v == "luganda", v == "oluganda":
		return LG
	default:
		return EN
	}
}

// Name returns the endonym, used in report headers.
func (l Lang) Name() string {
	switch l {
	case SW:
		return "Kiswahili"
	case LG:
		return "Luganda"
	default:
		return "English"
	}
}

// catalog holds every operator-visible string. Keys are stable identifiers so a
// missing translation is a visible test failure rather than silent English.
var catalog = map[string]map[Lang]string{
	"app.tagline": {
		EN: "Offline compliance copilot for savings groups and micro-SMEs",
		SW: "Msaidizi wa uzingatiaji unaofanya kazi nje ya mtandao kwa vikoba na biashara ndogo",
		LG: "Omulimba ogw'okukuuma amateeka ag'ekibiina ky'abasigalawo n'abavuzi abatonotono, ogufanya obufuzi oba kuggalawo",
	},
	"chat.banner": {
		EN: "Kanzu Agent — fully offline. Type :help for commands, :quit to exit.",
		SW: "Kanzu Agent — nje ya mtandao kabisa. Andika :help kwa amri, :quit kutoka.",
		LG: "Kanzu Agent — togatta ku mukutu. Wandiika :help eri ebiragiro, :quit okuva.",
	},
	"chat.prompt": {EN: "you", SW: "wewe", LG: "ggwe"},
	"chat.thinking": {
		EN: "working (deterministic checks first, then narration)…",
		SW: "inafanya kazi (ukaguzi wa uhakika kwanza, kisha maelezo)…",
		LG: "kola (okukebera okutegeeka olubereberye, n'emboozi)…",
	},
	"chat.help": {
		EN: "  :lang en|sw|lg switch language\n  :stats        show thermal + throughput stats\n  :plan         show the last execution plan\n  :quit         exit",
		SW: "  :lang en|sw|lg badilisha lugha\n  :stats        onyesha hali ya joto na kasi\n  :plan         onyesha mpango wa mwisho\n  :quit         toka",
		LG: "  :lang en|sw|lg kyusa olulimi\n  :stats        laga obubugumu n'omuvudde\n  :plan         laga entegeka eyasooka\n  :quit         vaamu",
	},
	"plan.header":     {EN: "EXECUTION PLAN", SW: "MPANGO WA UTEKELEZAJI", LG: "ENTEGEKA Y'OKUKOLA"},
	"evidence.header": {EN: "DETERMINISTIC EVIDENCE", SW: "USHAHIDI WA UHAKIKA", LG: "OBUKAKAFU OBUTEGEEKA"},
	"report.header":   {EN: "SUSPICIOUS ACTIVITY NOTE", SW: "TAARIFA YA SHUGHULI ZA KUTILIWA SHAKA", LG: "PPAPULA Y'EBIKOLWA EBITEEBEREZEBWA"},
	"report.subject":  {EN: "Member", SW: "Mwanachama", LG: "Memba"},
	"report.period":   {EN: "Period", SW: "Kipindi", LG: "Obudde"},
	"report.typology": {EN: "Typology", SW: "Aina ya hatari", LG: "Engeri y'akabi"},
	"report.findings": {EN: "Findings", SW: "Matokeo", LG: "Ebirabika"},
	"report.citations": {
		EN: "Sources consulted (local knowledge base)",
		SW: "Vyanzo vilivyotumika (hifadhi ya maarifa ya ndani)",
		LG: "Ensibuko ezakozesebwa (omusingi gw'okumanya ogw'omunda)",
	},
	"report.disclaimer": {
		EN: "Generated on-device by Kanzu Agent. Findings are produced by deterministic rules; the narrative is model-assisted. A human compliance officer must review and sign before any regulatory filing. No accusation of criminal conduct is implied.",
		SW: "Imetayarishwa kwenye kifaa hiki na Kanzu Agent. Matokeo yanatokana na kanuni za uhakika; maelezo yameandikwa kwa msaada wa modeli. Afisa wa uzingatiaji lazima aipitie na kuisaini kabla ya kuwasilishwa kwa mdhibiti. Hakuna tuhuma ya uhalifu inayodokezwa.",
		LG: "Kyakolebwa ku kyuma kino Kanzu Agent. Ebirabika bivudde mu mateeka ageetegeka; ebigambo byawandiikibwa n'obuyambi bwa mudeli. Omukungu w'okukuuma amateeka alina okubikebera n'okubissaako omukono nga tebinnaweerezebwa eri abafuzi. Tewali memba avunaanibwa musango.",
	},
	"alerts.none": {
		EN: "No rule triggered for the requested window. Nothing to escalate.",
		SW: "Hakuna kanuni iliyochochewa katika kipindi kilichoombwa. Hakuna la kupandisha.",
		LG: "Tewali teeka lyayasibwa mu bbanga eryasabibwa. Tewali kya kutwala mu maaso.",
	},
	"alerts.count":  {EN: "alerts raised", SW: "ilani zilizotolewa", LG: "endagamukutu ezayasibwa"},
	"severity.high": {EN: "high", SW: "kubwa", LG: "nnene"},
	"severity.med":  {EN: "medium", SW: "wastani", LG: "wa mu makkati"},
	"severity.low":  {EN: "low", SW: "ndogo", LG: "tonotono"},
	"inbox.empty": {
		EN: "Queue empty. Messages accepted offline are processed here in batches.",
		SW: "Foleni ni tupu. Ujumbe uliopokewa nje ya mtandao unashughulikiwa hapa kwa makundi.",
		LG: "Olukalala terimu kintu. Obubaka obwakkirizibwa nga tewali mukutu bukolebwa wano mu bibinja.",
	},
	"inbox.queued":    {EN: "queued for delivery when a link is available", SW: "imepangwa kutumwa mtandao utakapopatikana", LG: "kyategekedde okuweerezebwa nga omukutu gubaddewo"},
	"model.offline":   {EN: "model weights missing — deterministic findings only", SW: "modeli haipo — matokeo ya kanuni pekee", LG: "obuzito bwa mudeli bubuze — ebirabika by'amateeka bwokka"},
	"thermal.cooling": {EN: "thermal guard: pausing to cool", SW: "ulinzi wa joto: inasimama ipoe", LG: "okusuubirira obubugumu: okusiima okuzimba"},
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
	"deposit":     {"deposit", "deposits", "amana", "kuweka", "weka", "obutunzi", "teereka"},
	"withdrawal":  {"withdrawal", "withdraw", "kutoa", "uchukuzi", "kuchomoa", "okuggyawo", "ggyawo"},
	"transfer":    {"transfer", "remittance", "uhamisho", "kutuma", "hawala", "okutuma", "okusindika"},
	"cash":        {"cash", "currency", "fedha", "pesa", "taslimu", "ensimbi", "sente"},
	"loan":        {"loan", "credit", "mkopo", "mikopo", "enjatula", "okweyambisako"},
	"savings":     {"savings", "share", "akiba", "hisa", "okutereka", "ekyama"},
	"member":      {"member", "customer", "mwanachama", "wanachama", "mteja", "memba", "bamemba", "munnakibiina"},
	"account":     {"account", "akaunti", "hesabu", "akawunti"},
	"suspicious":  {"suspicious", "unusual", "kutiliwa", "shaka", "isiyo", "kawaida", "tuhuma", "ensobi", "obupooza"},
	"structuring": {"structuring", "smurfing", "kugawanya", "mgawanyo", "kuvunja", "okugabanya", "okusalamu"},
	"threshold":   {"threshold", "limit", "kiwango", "kikomo", "ukomo", "ekigatte", "omugereka"},
	"reporting":   {"reporting", "report", "kuripoti", "taarifa", "ripoti", "ppapula", "okuwanika"},
	"laundering":  {"laundering", "utakatishaji", "kusafisha", "okwoza", "okukoza"},
	"terrorism":   {"terrorism", "financing", "ugaidi", "ufadhili", "obwetamivu", "okuteeka"},
	"kyc":         {"kyc", "identity", "identification", "utambulisho", "uthibitisho", "obwenkanya", "okumanya"},
	"risk":        {"risk", "hatari", "athari", "obucwezi", "engeri"},
	"dormant":     {"dormant", "inactive", "tulivu", "isiyotumika", "eyekwatako", "ekiri"},
	"velocity":    {"velocity", "frequency", "kasi", "mara", "mfululizo", "omuvudde", "emirundi"},
	"crossborder": {"cross-border", "foreign", "nje", "kigeni", "mpakani", "ensi", "omuzanvu"},
	"pep":         {"pep", "politically", "exposed", "kisiasa", "mwanasiasa", "omukulembeze", "gavumenti"},
	"sacco":       {"sacco", "cooperative", "chama", "ushirika", "kikoba", "vikoba", "ekibiina", "omwoyo"},
	"mobilemoney": {"mpesa", "m-pesa", "mobile", "wallet", "simu", "kapu", "mtn", "airtel", "efunze"},
	"compliance":  {"compliance", "uzingatiaji", "ufuatiliaji", "okukuuma", "amateeka"},
	"audit":       {"audit", "ukaguzi", "hesabu", "okusomesa", "okukebera"},
	"committee":   {"committee", "board", "kamati", "bodi", "ekibiina", "obukiiko"},
	"week":        {"week", "weekly", "wiki", "sabbiiti"},
	"month":       {"month", "monthly", "mwezi", "omwezi"},
	"today":       {"today", "leo", "leero"},
	"draft":       {"draft", "write", "andika", "tayarisha", "andaa", "wandiika", "tegeka"},
	"explain":     {"explain", "why", "eleza", "kwanini", "nini", "tegeeza", "lwaki"},
	"scan":        {"scan", "check", "flag", "chunguza", "kagua", "angalia", "onyesha", "kebera", "laba"},
	"summary":     {"summary", "overview", "muhtasari", "jumla", "obugumba", "okubikka"},
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
	// Luganda stopwords
	"ne": true, "nga": true, "mu": true, "ku": true, "oba": true, "era": true,
	"nti": true, "eri": true, "bw": true, "kw": true, "gw": true,
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
	var sw, en, lg int
	for _, t := range toks {
		if swMarkers[t] {
			sw++
		}
		if enMarkers[t] {
			en++
		}
		if lgMarkers[t] {
			lg++
		}
	}
	if lg > en && lg > sw {
		return LG
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

// lgMarkers contains Luganda-specific tokens for language detection.
// Luganda is spoken by ~16M people in the Buganda region of Uganda.
var lgMarkers = buildMarkers([]string{
	"obutunzi", "teereka", "sente", "ensimbi", "akawunti", "mupolisi", "bapolisi",
	"okugabanya", "okukuuma", "amateeka", "kebera", "ekibiina", "sabbiiti",
	"omwezi", "leero", "wandiika", "tegeka", "tegeeza", "lwaki", "laba",
	"ppapula", "ensobi", "obucwezi", "omugereka", "okwoza", "mtn", "airtel",
	"gavumenti", "omukulembeze", "efunze", "omwoyo",
})

func buildMarkers(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
