#!/usr/bin/env python3
"""
Download HF AML/KYC/compliance datasets and convert them to llama.cpp
ChatML-format JSONL for LoRA fine-tuning of Kanzu Agent.

Datasets used:
  - Azfarhashmi/adaption-financial-crime-reasoning-traces (500 compliance reasoning traces)
  - SaiPavankumar22/FinWise (1000 multi-turn finance dialogues)
  - sovereign-forger/kyc-aml-sample-data (300 KYC/AML profiles)
  - electricsheepafrica/africa-mobile-money-fraud-dataset (200 Africa mobile money fraud cases)
  - electricsheepafrica/africa-fintech-neobank-dataset (200 African fintech fraud cases)
  - preetisheoran/nfpc-parquet-dataset (200 mule account profiles)
  - HaseebDev/aml_transaction_anomalies (100 AML transaction anomaly narratives)

Each dataset is converted to ChatML format with user/assistant pairs.
Tabular datasets (fraud, mule accounts) are converted to natural-language QA pairs.

Output:
  training/training.jsonl          - full deduplicated dataset (ChatML)
  training/train.jsonl             - 90% training split
  training/test.jsonl              - 10% held-out test split

Usage:
  pip install datasets pandas
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
        "name": "financial-crime-reasoning",
        "repo": "Azfarhashmi/adaption-financial-crime-reasoning-traces",
        "split": "train",
        "fields": None,
        "max_examples": 500,
        "format": "auto",
    },
    {
        "name": "finwise-dialogue",
        "repo": "SaiPavankumar22/FinWise",
        "split": "train",
        "fields": None,
        "max_examples": 1000,
        "format": "auto",
    },
    {
        "name": "kyc-aml-samples",
        "repo": "sovereign-forger/kyc-aml-sample-data",
        "split": "train",
        "fields": None,
        "max_examples": 300,
        "format": "auto",
    },
    {
        "name": "africa-mobile-money-fraud",
        "repo": "electricsheepafrica/africa-mobile-money-fraud-dataset",
        "split": "train",
        "fields": None,
        "max_examples": 200,
        "format": "auto",
    },
    {
        "name": "africa-fintech-neobank-fraud",
        "repo": "electricsheepafrica/africa-fintech-neobank-dataset",
        "split": "train",
        "fields": None,
        "max_examples": 200,
        "format": "auto",
    },
    {
        "name": "nfpc-parquet",
        "repo": "preetisheoran/nfpc-parquet-dataset",
        "split": "train",
        "fields": None,
        "max_examples": 200,
        "format": "auto",
    },
    {
        "name": "aml-transaction-anomalies",
        "repo": "HaseebDev/aml_transaction_anomalies",
        "split": "train",
        "fields": None,
        "max_examples": 100,
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


def convert_mfraud(item):
    """Convert electricsheepafrica/africa-mobile-money-fraud tabular row to ChatML."""
    ft = item.get("fraud_type", "")
    tt = item.get("transaction_type", "")
    amt = item.get("transaction_amount", "")
    country = item.get("country", "")
    plat = item.get("mobile_money_platform", "")
    fraud_detected = str(item.get("fraud_detected", "")).strip()
    label = str(item.get("label", "")).strip()
    # Build natural-language description of the transaction
    user_text = (
        f"A mobile money transaction in {country} via {plat}:\n"
        f"  Transaction type: {tt}\n"
        f"  Amount: {amt}\n"
        f"  Fraud type (if any): {ft}\n"
        f"  Fraud detected: {fraud_detected} ({label})\n\n"
        f"Based on these features, is this transaction suspicious? "
        f"Explain your reasoning in terms of money laundering and fraud risk indicators."
    )
    # Determine if fraudulent and build assistant response
    if fraud_detected and fraud_detected.lower() not in ("0", "false", "no", ""):
        assistant_text = (
            f"This transaction shows indicators of potential fraud/money laundering. "
            f"The fraud type is: {ft}. "
            f"Key risk factors: the transaction pattern, amount, and platform context "
            f"warrant further review under AML/CFT suspicious transaction reporting obligations."
        )
    else:
        assistant_text = (
            f"This transaction does not show clear indicators of fraud or money laundering "
            f"based on the available features. No suspicious activity flag is raised."
        )
    return [
        {"role": "user", "content": user_text},
        {"role": "assistant", "content": assistant_text},
    ]


def convert_neobank(item):
    """Convert electricsheepafrica/africa-fintech-neobank tabular row to ChatML."""
    tx_type = item.get("tx_type", "")
    amt = item.get("amount", "")
    country = item.get("country", "")
    plat = item.get("platform", "")
    fraud_type = item.get("fraud_type", "")
    label = str(item.get("label", "")).strip()
    velocity_1h = item.get("velocity_1h", "")
    velocity_24h = item.get("velocity_24h", "")
    distinct_receivers = item.get("distinct_receivers_24h", "")
    user_text = (
        f"A fintech transaction in {country} via {plat}:\n"
        f"  Transaction type: {tx_type}\n"
        f"  Amount: {amt}\n"
        f"  Velocity (1h): {velocity_1h}\n"
        f"  Velocity (24h): {velocity_24h}\n"
        f"  Distinct receivers (24h): {distinct_receivers}\n"
        f"  Fraud type (if any): {fraud_type}\n"
        f"  Label: {label}\n\n"
        f"Based on these features, is this transaction suspicious? "
        f"Explain your reasoning in terms of money laundering and fraud risk indicators."
    )
    if label and label.lower() not in ("0", "false", "no", ""):
        assistant_text = (
            f"This transaction shows indicators of potential fraud/money laundering. "
            f"The fraud type is: {fraud_type}. "
            f"Key risk factors: the transaction pattern, amount, velocity, and platform context "
            f"warrant further review under AML/CFT suspicious transaction reporting obligations."
        )
    else:
        assistant_text = (
            f"This transaction does not show clear indicators of fraud or money laundering "
            f"based on the available features. No suspicious activity flag is raised."
        )
    return [
        {"role": "user", "content": user_text},
        {"role": "assistant", "content": assistant_text},
    ]


def convert_nfpc(item):
    """Convert preetisheoran/nfpc-parquet mule account data to ChatML."""
    account_id = item.get("account_id", "")
    is_mule = str(item.get("is_mule", "")).strip()
    mule_flag_date = item.get("mule_flag_date", "")
    alert_reason = item.get("alert_reason", "")
    flagged_by_branch = item.get("flagged_by_branch", "")
    user_text = (
        f"Account analysis for {account_id}:\n"
        f"  Is mule account: {is_mule}\n"
        f"  Mule flag date: {mule_flag_date}\n"
        f"  Alert reason: {alert_reason}\n"
        f"  Flagged by branch: {flagged_by_branch}\n\n"
        f"Based on these features, is this account involved in money mule activity? "
        f"Explain your reasoning."
    )
    if is_mule and is_mule.lower() not in ("0", "false", "no", ""):
        assistant_text = (
            f"This account shows indicators of money mule activity. "
            f"Mule flag date: {mule_flag_date}. "
            f"Alert reason: {alert_reason}. "
            f"This account warrants further investigation under AML/CFT suspicious transaction reporting obligations."
        )
    else:
        assistant_text = (
            f"This account does not show clear indicators of money mule activity "
            f"based on the available features. No suspicious activity flag is raised."
        )
    return [
        {"role": "user", "content": user_text},
        {"role": "assistant", "content": assistant_text},
    ]


def convert_auto(item):
    """Auto-detect format from item keys and produce ChatML message pairs."""
    keys = set(item.keys())
    # Alpaca-style (instruction/input/output)
    if "instruction" in keys or "input" in keys:
        return convert_alpaca(item, {})
    # OpenAI chat format (messages list)
    if "messages" in keys:
        msgs = item["messages"]
        # Drop rows where assistant never responds (FinWise has many system+user-only rows)
        has_user = any(m.get("role") == "user" for m in msgs)
        has_assistant = any(m.get("role") == "assistant" for m in msgs)
        if has_user and has_assistant:
            return msgs
        return None
    # FinWise 'text' column with ### USER: / ### ASSISTANT: markers
    if "text" in keys and "category" in keys:
        return convert_openai(item, {"content": "text"})
    # prompt/completion pairs (Azfarhashmi financial-crime-reasoning)
    if "prompt" in keys and "completion" in keys:
        prompt = item.get("prompt") or ""
        completion = item.get("completion") or ""
        if prompt.strip() and completion.strip():
            return [
                {"role": "user", "content": prompt.strip()},
                {"role": "assistant", "content": completion.strip()},
            ]
        return None
    # AML transaction anomalies (HaseebDev): transaction_pattern/investigator_rationale
    if "transaction_pattern" in keys and "investigator_rationale" in keys:
        tp = item.get("transaction_pattern", "").strip()
        ir = item.get("investigator_rationale", "").strip()
        if tp and ir:
            return [
                {"role": "user", "content": tp},
                {"role": "assistant", "content": ir},
            ]
        return None
    # KYC sample data (sovereign-forger): narrative bio + risk rating
    if "narrative_bio" in keys and "kyc_risk_rating" in keys:
        nb = item.get("narrative_bio", "").strip()
        rr = item.get("kyc_risk_rating", "").strip()
        if nb and rr:
            return [
                {"role": "user", "content": f"A customer profile: {nb}\n\nAssess the KYC risk rating and explain your reasoning."},
                {"role": "assistant", "content": f"KYC Risk Rating: {rr}."},
            ]
        return None
    # Tabular fraud data (electricsheepafrica mobile money / neobank): convert features to prose
    if "fraud_type" in keys and "transaction_amount" in keys and "transaction_type" in keys:
        return convert_mfraud(item)
    if "tx_type" in keys and "amount" in keys and "fraud_type" in keys:
        return convert_neobank(item)
    # nfpc mule account detection
    if "account_id" in keys and "is_mule" in keys:
        return convert_nfpc(item)
    return None


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
        if msgs is None:
            continue
        roles = {m.get("role", "") for m in msgs}
        if "user" in roles and "assistant" in roles:
            examples.append(msgs)
    return examples


def main():
    out_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)))
    out_path = os.path.join(out_dir, "training.jsonl")
    train_path = os.path.join(out_dir, "train.jsonl")
    test_path = os.path.join(out_dir, "test.jsonl")

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

    total = len(all_examples)
    print(f"\n{'='*60}")
    print(f"Total unique examples: {total}")

    # --- train / test split (90/10) ---
    split_idx = int(total * 0.9)
    train_examples = all_examples[:split_idx]
    test_examples = all_examples[split_idx:]

    for label, path, examples in [
        ("training.jsonl", out_path, all_examples),
        ("train.jsonl", train_path, train_examples),
        ("test.jsonl", test_path, test_examples),
    ]:
        with open(path, "w", encoding="utf-8") as f:
            for ex in examples:
                f.write(json.dumps(ex, ensure_ascii=False))
                f.write("\n")
        print(f"Wrote {path}: {len(examples)} examples")

    print(f"\nDataset breakdown:")
    for ds_config in DATASETS:
        print(f"  - {ds_config['name']} ({ds_config['repo']}) [{ds_config['max_examples']} max]")
    print(f"\nTrain: {len(train_examples)} | Test: {len(test_examples)} | Total: {total}")


if __name__ == "__main__":
    main()
