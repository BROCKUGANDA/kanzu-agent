# Kanzu Agent — Offline AML Compliance Copilot for Ugandan SACCOs

> **ADTC Africa Deep Tech Challenge 2026** entry
> **Team:** BROCKUGANDA | **Team ID:** 1148777 | **Region:** Uganda

## The Problem

Ugandan Savings and Credit Cooperatives Societies (SACCOs) are legally required
to file Anti-Money Laundering reports to the Financial Intelligence Authority.
Most SACCOs are offline, resource-constrained, and lack the technical capacity
to run cloud-based AI tools. Kanzu Agent brings **offline, trilingual AI
assistance** to the field officer's laptop—no internet required.

## What It Does

A TUI (terminal) agent that runs entirely on-device:

- **Trilingual chat** — English, Kiswahili, Luganda
- **Deterministic AML rule engine** — 8 rules covering velocity spikes, watchlist
  exposure, structuring, threshold breaches, dormant-account reactivation, and
  more (zero model involvement in compliance decisions)
- **Cross-lingual KB retrieval** — 3,000 chunk regulatory corpus indexed per
  language; queries retrieve across all three
- **Report drafting** — after rules fire, the LLM drafts a human-readable
  compliance note (the model narrates findings; it cannot add, remove, or
  reclassify alerts)
- **Offline-first messaging** — inbox queue for when agents go back online

## Quick Start

```bash
# 1. Clone + install dependencies
git clone https://github.com/BROCKUGANDA/kanzu-agent.git
cd kanzu-agent
bash scripts/setup.sh          # installs Go, llama.cpp, Python deps

# 2. Download the model (Qwen2.5-1.5B Q4_K_M, ~1 GB)
bash download_model.sh

# 3. Initialise the local SQLite ledger + KB index
kanzu init

# 4. Run the TUI
kanzu serve
```

### Quick Commands (outside the TUI)

```bash
kanzu doctor                        # check all wiring: model, ledger, corpus, thermal
kanzu ask -lang en "flag suspicious transactions this week"
kanzu ask -lang sw "chunguza miamala ya wiki hii"
kanzu ask -lang lg "kebera ebyenfuna bya mwezi"
kanzu send "<peer> <message>"       # enqueue an offline message
kanzu policy set <key> <value>      # adjust thresholds
```

## TUI Navigation

Press <kbd>Tab</kbd> / <kbd>Shift+Tab</kbd> to cycle sections, or
<kbd>1</kbd>–<kbd>8</kbd> for direct jump. <kbd>Ctrl+L</kbd> cycles language.

| # | Section | Purpose |
|---|---------|---------|
| 💬 | **Chat** | Ask the agent in EN/SW/LG |
| ⚡ | **Quick Scan** | Deterministic rule engine results |
| 📋 | **Reports** | Draft suspicious-activity files |
| 👥 | **Members** | SACCO member register + KYC |
| 📥 | **Inbox Queue** | Offline messages (in/out) |
| 🔍 | **Knowledge Base** | Indexed regulatory corpus |
| ⚙️ | **Policy** | Threshold configuration |
| 🩺 | **Doctor** | System health checks |

## Architecture

```
cmd/kanzu/           CLI entry + TUI bootstrap
internal/
  agent/             Planner (PLAN → GATE → EVIDENCE → NARRATE)
  rules/             Deterministic AML rule engine (SQL-based)
  ledger/            SQLite schema + migrations
  llm/               llama.cpp CGo bindings + prompt runner
  tui/               Bubble Tea full-screen interface
  i18n/              Trilingual prompt templates + translations
  thermal/           CPU duty-cycle governor (70% target)
  rag/               Cross-lingual KB indexing (FAISS-style)
knowledge/           Regulatory corpus (EN/SW/LG chunks)
  en/                1,000 chunks — Uganda AML Act, FAA guidelines
  sw/                1,000 chunks — Swahili regulatory translations
  lg/                1,000 chunks — Luganda community guidance
var/prompts/         6 versioned system prompts (planning + narration × 3 langs)
```

### Design Principle: Deterministic Compliance, LLM Narration

The rule engine (`internal/rules/engine.go`) is the sole authority for
compliance decisions. It runs SQL queries against the local SQLite ledger and
produces structured alerts. The LLM (`internal/llm/llama.go`) only narrates
those alerts into human-readable text using the PLAN → GATE → EVIDENCE → NARRATE
workflow. The model **cannot** add, remove, or reclassify alerts.

## Performance (ADTC Standard Laptop)

| Metric | Value |
|--------|-------|
| Model | Qwen2.5-1.5B-Instruct Q4_K_M |
| Throughput | 12.58 tok/s |
| First-token latency | 15.7 s |
| Accuracy (arc_easy, acc_norm) | 0.76 (50 samples) |
| Seed | 42 |
| Git commit | 8b8e8902fe88 |
| RAM | 1.7 GB peak (within 8 GB budget) |
| Thermal | No throttling (70% duty cycle governor) |

Full results in [`submission.json`](submission.json). Methodology in
[`docs/demo/record_profiler.md`](docs/demo/).

## Demo Video

A 2-minute walkthrough (`docs/demo/kanzu-agent-demo.mp4`) covers:

- `kanzu doctor` — all green checks
- English: flag suspicious transactions → 3 high-risk alerts, 2.4M UGX
- Kiswahili: "Chunguza miamala ya wiki hii" → alert structuring flagged
- Luganda: "Kebera ebyenfuna bya mwezi" → 5 suspicious items identified
- Profiler results summary

## Documentation

- **[REPORT.md](REPORT.md)** — full technical report (architecture, datasets,
  training, offline verification, trilingual strategy, submission checklist)
- **[AGENTS.md](AGENTS.md)** — contributor guide + development commands
- **[docs/](docs/)** — getting started, environment variables, architecture index
- **[.github/workflows/](.github/)** — CI (Spectral lint, Docker build, Go tests)

## Project Structure

```
kanzu-agent/
├── cmd/kanzu/              # CLI + TUI entry
├── internal/               # Go packages (agent, rules, tui, i18n, llm...)
├── knowledge/              # Regulatory corpus (en/sw/lg)
├── var/prompts/            # System prompt templates
├── docs/                   # Documentation + demo video
├── fixtures/               # Test fixtures
├── scripts/                # Setup, profiling, Docker helpers
├── vendor/llama.cpp/       # llama.cpp vendored (prebuilt binary)
├── model/                  # Downloaded GGUF weights (.gitignored)
├── Dockerfile              # Linux/amd64 production image
├── docker-compose.yml      # Quick local stack
└── submission.json         # ADTC profiler output
```

## Technical Stack

- **Runtime:** Go 1.22+ (CGo for llama.cpp FFI)
- **LLM:** Qwen2.5-1.5B-Instruct Q4_K_M via llama.cpp (offline)
- **TUI:** Charmbracelet Bubble Tea + Lipgloss
- **Ledger:** SQLite (SQLCipher-compatible)
- **Rules:** SQL queries, no external dependencies
- **Container:** Docker linux/amd64
- **License:** MIT
