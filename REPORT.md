# Kanzu Agent — Offline Compliance Copilot for East African SACCOs

**Team:** SET_ME_devpost_team_id
**Domain:** autonomous_ai_agents
**Model:** Qwen2.5-1.5B-Instruct-Q4_K_M (bartowski GGUF, 338 tensors)
**Runtime:** llama.cpp (subprocess, CPU-only, mmap-backed)

---

## 1. Problem Definition

Savings and Credit Cooperative Organisations (SACCOs) in Kenya hold over KES 1.4 trillion in member deposits, serving populations that formal banks do not reach. Under the **Proceeds of Crime and Anti-Money Laundering Act (2009)** and SASRA supervision, every SACCO must detect suspicious transaction patterns and report them to the **Financial Reporting Centre (FRC)**.

The eight typologies a compliance officer must watch for include:

| Rule | Typology | Core signal |
|------|----------|-------------|
| R01 | Structuring (deposit splitting) | Aggregate exceeds threshold; each component does not |
| R02 | Transaction velocity spike | Window value far exceeds member's own baseline |
| R03 | Repeated round-figure amounts | Identical round figures repeat suspiciously |
| R04 | Dormant-account reactivation | Long-dormant account suddenly active |
| R05 | Rapid pass-through (layering) | High % of incoming value exits within 48 h |
| R06 | Higher-risk jurisdiction exposure | Transactions involve watchlisted countries |
| R07 | KYC tier limit breach | Activity exceeds verified KYC ceiling |
| R08 | Threshold hugging | Amounts clustered just below reporting threshold |

Most SACCOs operate **offline** — no reliable internet, no cloud compliance tools, and no trained AML officer on staff. Kanzu Agent solves this: an **on-device, bilingual (English + Kiswahili)** compliance copilot that runs on a budget laptop, requires zero network access, and produces audit-trail-quality suspicious-activity notes.

---

## 2. Constraints

| Constraint | Value | Rationale |
|------------|-------|-----------|
| RAM | 8 GB | ADTC Standard Laptop spec |
| Network | None at runtime | SACCOs are offline; strongest proof is network-namespace isolation |
| Peak RSS (model) | ~1.2 GB | Q4_K_M of 1.5B model + mmap overhead |
| Thermal | Laptop-grade CPU | No active cooling; duty cycling prevents thermal throttle |
| Cost | 0 | Open-source model, no API keys, no cloud |
| Accuracy weight | 50% of S_total | ADTC scoring: S_total = 0.50·S_acc + 0.30·S_perf + 0.20·S_eff − P_thermal |
| Profiler fraud check | ±15% params_match | adtc-profiler validates claimed parameter count against tensor table |

---

## 3. Design Alternatives

### 3.1 Model Selection

| Candidate | Verdict |
|-----------|---------|
| Qwen2.5-0.5B-Instruct | Too small for structured JSON planning; high parse-failure rate |
| **Qwen2.5-1.5B-Instruct** | **Chosen.** Best multilingual quality at 1–2B class; native Kiswahili support |
| Qwen2.5-3B-Instruct | Q4_K_M ≈ 1.8 GB GGUF + 1.5 GB RSS → exceeds 8 GB budget with OS overhead |
| Llama-3.2-1B-Instruct | Weaker multilingual; no official Kiswahili token coverage |

### 3.2 GGUF Build Selection

Two public Q4_K_M builds of Qwen2.5-1.5B-Instruct exist on Hugging Face:

| Build | Tensors | Param sum | File size | params_match vs "1.5B" |
|-------|---------|-----------|-----------|------------------------|
| Qwen (official) | 339 | 1,777,088,000 | 1,117 MB | **FAIL** (+18.5%) |
| **bartowski** | **338** | **1,543,714,304** | **986 MB** | **PASS** (+2.9%) |

Qwen's official build materialises `output.weight` as a separate tensor instead of tying it to `token_embd.weight`. The profiler derives parameter count from the tensor table (since `general.parameter_count` is absent from both files) and applies a two-sided 15% fraud check. The official build would score `params_match = false` against any claim consistent with the model card.

The bartowski build ties the tensor, sums to 1.54B (matching the card), is 155 MB lighter in peak RSS, and benchmarks within one standard deviation on generation throughput. **Verify locally with `kanzu doctor`.**

### 3.3 Architecture: Rules-First Hybrid

| Approach | Pros | Cons |
|----------|------|------|
| Pure LLM (reads raw ledger) | Flexible | Too slow for 50+ transactions; hallucination risk; 8 GB can't hold large context |
| Pure rule engine | Fast, provably correct | Can't produce natural-language reports officers can file |
| **Rules + LLM (chosen)** | **Deterministic flags are load-bearing; model only narrates** | Two moving parts; but degraded mode works without model |

The pairing is **load-bearing, not cosmetic**: removing the compliance layer removes the product; removing the model leaves a working command-line auditor (`-no-model` flag). The system prompt constrains the model:

> *Findings were produced by a deterministic rule engine. Do not add findings, remove findings, or change a severity.*

### 3.4 Inference Strategy

**Subprocess per request** (stateless) rather than persistent `llama-server`:

- A loopback socket is still a socket — it puts `net/http` in the binary, weakens the offline claim, and breaks the strongest available proof (running inside `unshare -n` with no interfaces at all).
- Pipes have none of those properties.
- The GGUF is mmap'd, so the second and later loads come from the page cache in ~200 ms.
- Between requests the agent's resident set falls back to ~20 MB of Go instead of holding ~1.2 GB of weights hostage while an operator reads a report.
- Every inference is reproducible from its saved prompt file — exactly what a compliance audit trail needs.

---

## 4. Benchmark Results

> **Note:** Sections 4.2–4.3 are dev-machine measurements (i7-1255U, 16 GB RAM, Windows 11). Official profiler results from the ADTC Standard Laptop should replace the `SET_ME` placeholders in §4.4.

### 4.1 Model Characteristics

| Metric | Value |
|--------|-------|
| Model | Qwen2.5-1.5B-Instruct-Q4_K_M |
| GGUF version | 3 |
| Architecture | qwen2 |
| Quantization | Q4_K_M |
| Tensor count | 338 |
| Measured parameters | 1,543,714,304 |
| File size | 986,048,768 bytes (940 MiB) |
| Context length | 32,768 tokens |
| params_match | **true** (claimed 1.5B, measured 1.54B, δ = +2.9%) |

### 4.2 Dev-Machine Throughput

| Metric | Value |
|--------|-------|
| Prompt processing | ~90 tok/s |
| Generation | ~22 tok/s |
| Model load (cold) | ~1.5 s (page-cache backed) |
| Model load (warm) | ~200 ms |
| Threads | 4 |

### 4.3 Deterministic Pipeline

| Metric | Value |
|--------|-------|
| Rules evaluated | 8 (R01–R08) |
| Test window | 7 days (2026-08-15 → 2026-08-21) |
| Transactions scanned | 22 of 50 |
| Alerts raised | 9 (5 HIGH, 3 MEDIUM, 1 LOW) |
| RAG retrieval | Cross-language (en ↔ sw) |
| Knowledge chunks | 35 |
| Knowledge sources | 5 documents (3 English, 2 Kiswahili) |

**Sample alerts from test window:**

| Severity | Rule | Member | Summary |
|----------|------|--------|---------|
| HIGH | R02_VELOCITY | Amina Wanjiru (M-001) | 15.0× baseline spike (KES 1,200,000 / 168 h vs KES 80,000 baseline) |
| HIGH | R01_STRUCTURING | Amina Wanjiru (M-001) | 5 deposits totalling KES 1.2M within 72 h; each below KES 1M threshold |
| HIGH | R06_CROSS_BORDER | Peter Kimani (M-004) | KES 650,000 involving watchlisted jurisdiction SY |
| HIGH | R05_RAPID_PASSTHROUGH | Grace Mueni (M-003) | 87% of incoming value exited within 48 h |
| HIGH | R06_CROSS_BORDER | Rehema Salim (M-009) | KES 120,000 involving watchlisted jurisdiction YE |
| MEDIUM | R03_ROUND_AMOUNT | Amina Wanjiru (M-001) | KES 240,000 repeated 5× |
| MEDIUM | R07_KYC_LIMIT | Halima Abdi (M-005) | KYC tier 1, activity KES 253,500 exceeds KES 200,000 ceiling |
| MEDIUM | R08_THRESHOLD_HUGGING | Sarah Njeri (M-007) | 2 transactions at 85–100% of KES 1M threshold |
| LOW | R03_ROUND_AMOUNT | Daniel Mwangi (M-006) | KES 50,000 repeated 3× |

### 4.4 ADTC Profiler Results

> Replace `SET_ME` values with official profiler output from the Standard Laptop.

| Metric | Value |
|--------|-------|
| S_accuracy | SET_ME |
| S_performance | SET_ME |
| S_efficiency | SET_ME |
| P_thermal | SET_ME |
| **S_total** | **SET_ME** |

---

## 5. Fine-Tuning Strategy

The model serves a **non-adjudicative role** (planning + narration), so fine-tuning targets compliance-domain fluency without altering the base model's reasoning. Training uses **Hugging Face datasets** with **llama.cpp's built-in LoRA support** (`train-text-lora`).

### 5.1 Datasets (from Hugging Face)

| Dataset | Size | Content |
|---------|------|---------|
| `Rakeshadasani/banking-finance-qa-dataset` | 3,002 rows | Instruction-response pairs for AML/KYC/compliance/regulatory QA |
| `Azfarhashmi/adaption-financial-crime-reasoning-traces` | ~500 rows | Reasoning traces for compliance officers across banking, trade finance, virtual assets |
| `SaiPavankumar22/FinWise` | ~1,000 rows | Multi-turn finance dialogues including AML compliance scenarios |
| `sovereign-forger/kyc-aml-sample-data` | ~300 rows | KYC/AML structured sample data |

### 5.2 Training Pipeline

```
training/prepare_datasets.py   → download from HF, convert to ChatML JSONL
training/train_lora.sh         → llama.cpp train-text-lora (rank 16, α 32)
```

**Steps:**

1. `python3 training/prepare_datasets.py` — downloads datasets via `huggingface_hub`, converts to ChatML-format JSONL (`training/training.jsonl`), deduplicates and shuffles
2. `bash training/train_lora.sh` — runs `llama-cli train-text-lora` with:
   - LoRA rank 16, alpha 32 (applied to q_proj, v_proj)
   - 2 epochs, learning rate 2e-4
   - Cutoff 256 tokens per example
   - Context 2048 (matching `_kanzu.context_tokens`)
3. Merged output: `model/Kanzu-Qwen2.5-1.5B-Q4_K_M-lora.gguf`
4. Re-verify: `kanzu doctor` — LoRA merge should not change tensor count or param sum

### 5.3 Expected Impact

- **Better compliance vocabulary** — FATF terminology, Kenyan regulatory citations, SACCO-specific language
- **More structured JSON plans** — reduced parse failures in the hybrid planner
- **Improved Kiswahili narration** — domain terms like *kugawanya miamala* (structuring) in natural output
- **Must NOT** degrade general instruction-following or introduce hallucinated compliance claims (guard enforced by system prompt)

---

## 6. Offline Verification

The agent makes **zero network calls at runtime**. This is verified mechanically by `scripts/verify_offline.sh`, which runs the full pipeline inside a Linux network namespace (`unshare -n`) where all `socket()` calls fail:

```bash
unshare -n --map-root-user bash -c '
    ip link set lo up 2>/dev/null || true
    ./bin/kanzu scan -days 7
    ./bin/kanzu ask "flag suspicious transactions this week"
    ./bin/kanzu report -days 7
'
```

The Go binary has no `net/http` imports in its runtime path (verified by `grep -r 'net/http'` over `cmd/` and `internal/`). All inference uses stdin/stdout pipes to `llama.cpp` — no loopback sockets.

---

## 7. Bilingual Capability

English and Kiswahili are **first-class**:

- **Knowledge base:** 3 English + 2 Kiswahili documents covering:
  - FATF Guidance on AML/CFT and Financial Inclusion (en)
  - Proceeds of Crime Act 2009, SASRA obligations (en)
  - Mwongozo wa FATF kuhusu AML/CFT (sw)
  - Mapendekezo ya FATF na wajibu wa kuripoti (sw)
- **RAG retrieval:** Cross-language — English queries find Kiswahili chunks and vice versa
- **UI:** Full i18n with `kanzu -lang sw` flag; all rule titles, alerts, and disclaimers translated
- **Narration:** Model generates compliance notes in whichever language the operator requests

---

## 8. Security Posture

| Property | Evidence |
|----------|----------|
| No network calls at runtime | Provable via `unshare -n`; `grep -r 'net/http'` shows no runtime imports |
| No API keys or cloud services | No configuration field for keys; model is a local GGUF file |
| No telemetry | No outgoing connections; `doctor` confirms `runtime_network_calls: none` |
| Immutable deterministic findings | Model prompt explicitly forbids adding/removing/reclassifying alerts |
| No criminal adjudication | System prompt: "Never state or imply that a member committed a crime" |
| Reproducible inference | Every prompt saved to `var/prompts/`; reviewer can replay any generation |
| Subprocess isolation | `llama.cpp` runs as a child with no privilege escalation |

---

## 9. Repository Structure

```
kanzu-agent/
├── cmd/kanzu/main.go          CLI entry point (11 subcommands)
├── internal/
│   ├── agent/                 Hybrid planner + execution loop
│   ├── config/                Metadata-driven config (env overrides)
│   ├── engine/                8 deterministic AML/CFT rules
│   ├── gguf/                  GGUF v3 parser (header, tensors, KV)
│   ├── i18n/                  English + Kiswahili translations
│   ├── ledger/                SQLite schema + queries
│   ├── llm/                   llama.cpp subprocess runner + thermal governor
│   ├── rag/                   Cross-language vector retrieval (FTS5)
│   ├── rules/                 Rule definitions R01–R08
│   └── thermal/               CPU thermal governor (ceiling/resume/duty)
├── knowledge/                 5 source documents (3 en, 2 sw)
├── fixtures/                  Seed data (9 members, 50 transactions)
├── model/                     Qwen2.5-1.5B-Instruct-Q4_K_M.gguf (gitignored)
├── scripts/
│   ├── setup_llama_cpp.sh     Build llama.cpp into vendor/
│   ├── verify_offline.sh      Prove zero network with unshare -n
│   └── run_profiler.sh        Run adtc-profiler → submission.json
├── training/
│   ├── prepare_datasets.py    Download HF datasets → ChatML JSONL
│   └── train_lora.sh          llama.cpp LoRA fine-tuning
├── var/                       Runtime state (gitignored: DB, prompts)
├── download_model.sh          Idempotent HF model fetch + sha256 verify
├── metadata.json              ADTC submission contract
├── REPORT.md                  This file
└── go.mod                     Go 1.22, modernc.org/sqlite (pure-Go)
```

---

## 10. Reproduction

```bash
# 1. Clone and build
git clone <repo-url> kanzu-agent && cd kanzu-agent
bash scripts/setup_llama_cpp.sh        # builds llama.cpp into vendor/

# 2. Fetch model
bash download_model.sh                 # idempotent; verifies sha256

# 3. Verify everything
go run ./cmd/kanzu doctor              # all green?

# 4. Run agent
go run ./cmd/kanzu ask "flag suspicious transactions this week and draft a compliance note"

# 5. Prove offline
bash scripts/verify_offline.sh         # runs inside unshare -n

# 6. (Optional) Fine-tune
python3 training/prepare_datasets.py   # download HF datasets
bash training/train_lora.sh            # LoRA with llama.cpp

# 7. Profile
bash scripts/run_profiler.sh           # → submission.json
```
