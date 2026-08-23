#!/usr/bin/env bash
set -uo pipefail
cd "/c/Users/HP/Desktop/kanzu agent"
mkdir -p "$PWD/docs/demo/frames"
OUT="$PWD/docs/demo/frames"

echo "=== Capturing demo outputs into $OUT ==="

go build -o bin/kanzu.exe ./cmd/kanzu
./bin/kanzu.exe doctor > "$OUT/doctor.txt" 2>&1
echo "doctor: $?"

./bin/kanzu.exe ask -lang en "Flag suspicious transactions from last week and draft a compliance note for the savings committee." > "$OUT/en.txt" 2>&1
echo "en: $?"

./bin/kanzu.exe ask -lang sw "Chunguza miamala ya wiki hii." > "$OUT/sw.txt" 2>&1
echo "sw: $?"

./bin/kanzu.exe ask -lang lg "Kebera ebyenfuna bya mwezi." > "$OUT/lg.txt" 2>&1
echo "lg: $?"

python -c "import json; d=json.load(open('submission.json')); print(f'throughput: {d[\"throughput\"][\"tokens_per_second_generation\"]} tok/s'); print(f'accuracy: {d[\"accuracy\"][0][\"score\"]}')" > "$OUT/profiler.txt" 2>&1
echo "profiler: $?"

bash scripts/verify_offline.sh > "$OUT/offline.txt" 2>&1 || true
echo "offline: $?"
echo "=== done ==="
ls "$OUT"
