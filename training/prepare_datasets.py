#!/usr/bin/env python3
"""
Download HF AML/KYC/compliance datasets and convert them to llama.cpp
ChatML-format JSONL for LoRA fine-tuning of Kanzu Agent.

Datasets used:
  - Rakeshadasani/banking-finance-qa-dataset (~3000 QA pairs)
  - Azfarhashmi/adaption-financial-crime-reasoning-traces (compliance reasoning)
  - SaiPavankumar22/FinWise (multi-turn finance dialogues with AML scenarios)
  - sovereign-forger/kyc-aml-sample-data (KYC/AML structured data)

Output: training/training.jsonl (deduplicated, shuffled, ChatML-formatted).

Usage:
  pip install datasets
  python3 training/prepare_datasets.py
"""

import json
import os
import random
import sys

KANZU_SYSTEM = (
    "You are Kanzu Agent, an offline compliance assistant for a Kenyan "
    "savings and credit cooperative (SACCO).\n\n"
    "You help officers detect suspicious transaction patterns and draft "
    "regulatory reports. You cite specific regulations and never adjudicate "
    "criminal guilt. You work from structured evidence produced by a "
    "deterministic rule engine.\n\n"
    "You respond in English or Kiswahili as requested."
)

DATASETS = [
    {
        "name": "banking-finance-qa",
        "repo": "Rakeshadasani/banking-finance-qa-dataset",
        "split": "train",
        "fields": {"instruction": "instruction", "input": "input", "output": "output"},
        "max_examples": 3000,
        "format": "alpaca",
    },
    {
        "name": "financial-crime-reasoning",
        "repo": "Azfarhashmi/adaption-financial-crime-reasoning-traces",
        "split": "train",
        "fields": None,
        "max_examples": 500,
        "format": "chatml",
    },
    {
        "name": "finwise-dialogue",
        "repo": "SaiPavankumar22/FinWise",
        "split": "train",
        "fields": {"content": "content"},
        "max_examples": 1000,
        "format": "openai",
    },
    {
        "name": "kyc-aml-samples",
        "repo": "sovereign-forger/kyc-aml-sample-data",
        "split": "train",
        "fields": None,
        "max_examples": 300,
        "format": "auto",
    },
]


def try_import_hf():
    """Try importing datasets. Print install instructions if missing."""
    try:
        import datasets  # noqa: F401
        return True
    except ImportError:
        pass
    try:
        import huggingface_hub  # noqa: F401
        return True
    except ImportError:
        pass
    print("ERROR: Neither 'datasets' nor 'huggingface_hub' is installed.")
    print("Install one of:")
    print("  pip install datasets")
    print("  pip install huggingface_hub")
    return False


def format_chatml(system, messages):
    """Format messages as ChatML string."""
    parts = [f"<|im_start|>system\n{system}<|im_end|>"]
    for msg in messages:
        role = msg["role"]
        content = msg["content"].strip()
        if not content:
            continue
        parts.append(f"<|im_start|>{role}\n{content}<|im_end|>")
    parts.append("<|im_start|>assistant\n")
    return "\n".join(parts)


def convert_alpaca(item, fields):
    """Convert Alpaca-style (instruction/input/output) to ChatML messages."""
    instruction = item.get(fields.get("instruction", "instruction"), "")
    inp = item.get(fields.get("input", "input"), "")
    output = item.get(fields.get("output", "output"), "")
    user_text = instruction
    if inp and inp.strip():
        user_text += f"\n\n{inp}"
    return [
        {"role": "user", "content": user_text.strip()},
        {"role": "assistant", "content": output.strip()},
    ]


def convert_openai(item, fields):
    """Convert OpenAI-style (role: content lines) to ChatML messages."""
    text = item.get(fields.get("content", "content"), "")
    messages = []
    current_role = None
    current_content = []
    for line in text.split("\n"):
        line_stripped = line.strip()
        line_lower = line_stripped.lower()
        if line_lower.startswith("system:") or line_lower.startswith("### system"):
            if current_role:
                messages.append({"role": current_role, "content": "\n".join(current_content)})
            current_role = "system"
            current_content = [line.split(":", 1)[-1].strip()]
        elif line_lower.startswith("user:") or line_lower.startswith("### user") or line_lower.startswith("human:"):
            if current_role:
                messages.append({"role": current_role, "content": "\n".join(current_content)})
            current_role = "user"
            current_content = [line.split(":", 1)[-1].strip()]
        elif line_lower.startswith("assistant:") or line_lower.startswith("### assistant") or line_lower.startswith("model:"):
            if current_role:
                messages.append({"role": current_role, "content": "\n".join(current_content)})
            current_role = "assistant"
            current_content = [line.split(":", 1)[-1].strip()]
        elif current_role is not None:
            current_content.append(line)
    if current_role:
        messages.append({"role": current_role, "content": "\n".join(current_content)})
    return [m for m in messages if m.get("content", "").strip()]


def convert_auto(item):
    """Auto-detect format from item keys."""
    keys = set(item.keys())
    if "instruction" in keys or "input" in keys:
        return convert_alpaca(item, {})
    if "messages" in keys:
        return item["messages"]
    if "conversation" in keys:
        conv = item["conversation"]
        if isinstance(conv, list):
            return conv
    return convert_openai(item, {})


def load_dataset(repo_name, split, max_examples, fmt, fields=None):
    """Load a dataset from HF and convert to message lists."""
    examples = []
    try:
        from datasets import load_dataset
        ds = load_dataset(repo_name, split=split)
        rows = list(ds)[:max_examples]
        print(f"  {repo_name}: loaded {len(rows)} examples")
    except Exception as e:
        print(f"  WARNING: Could not load {repo_name} via datasets: {e}")
        try:
            from huggingface_hub import hf_hub_download
            path = hf_hub_download(
                repo_id=repo_name,
                filename=f"{split}.parquet",
                repo_type="dataset",
            )
            import pandas as pd
            df = pd.read_parquet(path)
            rows = df.to_dict("records")[:max_examples]
            print(f"  {repo_name}: loaded {len(rows)} examples via parquet fallback")
        except Exception as e2:
            print(f"  ERROR: Could not load {repo_name}: {e2}")
            return examples

    converter = {
        "alpaca": lambda i: convert_alpaca(i, fields or {}),
        "openai": lambda i: convert_openai(i, fields or {}),
        "chatml": lambda i: i.get("messages", []),
        "auto": lambda i: convert_auto(i),
    }.get(fmt, convert_auto)

    for row in rows:
        msgs = converter(row)
        roles = {m["role"] for m in msgs}
        if "user" in roles and "assistant" in roles:
            examples.append(msgs)
    return examples


def main():
    out_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)))
    out_path = os.path.join(out_dir, "training.jsonl")

    if not try_import_hf():
        sys.exit(1)

    all_examples = []
    seen_hashes = set()

    for ds_config in DATASETS:
        name = ds_config["name"]
        print(f"\nLoading: {name} ({ds_config['repo']}) ...")
        examples = load_dataset(
            repo_name=ds_config["repo"],
            split=ds_config["split"],
            max_examples=ds_config["max_examples"],
            fmt=ds_config["format"],
            fields=ds_config["fields"],
        )
        new_count = 0
        for msgs in examples:
            chatml = format_chatml(KANZU_SYSTEM, msgs)
            h = hash(chatml)
            if h not in seen_hashes:
                seen_hashes.add(h)
                all_examples.append(chatml)
                new_count += 1
        print(f"  {name}: {new_count} unique examples after dedup")

    random.seed(42)
    random.shuffle(all_examples)

    with open(out_path, "w", encoding="utf-8") as f:
        for ex in all_examples:
            f.write(json.dumps(ex, ensure_ascii=False))
            f.write("\n")

    print(f"\n{'='*60}")
    print(f"Written to {out_path}")
    print(f"Total unique examples: {len(all_examples)}")
    for ds_config in DATASETS:
        print(f"  - {ds_config['name']} ({ds_config['repo']})")


if __name__ == "__main__":
    main()
