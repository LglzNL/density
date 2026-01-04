#!/usr/bin/env bash
set -euo pipefail

# DENSITY Demo Script (Linux)
# - baut densityctl
# - führt Baseline (KSM aus) vs. DENSITY (KSM an) aus
#
# Hinweis: KSM-Toggles brauchen Root. Dieses Script nutzt sudo.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p bin results

echo "== Build =="
go build -o ./bin/densityctl ./cmd/densityctl

TS="$(date +%Y%m%d_%H%M%S)"
OUT="results/demo_${TS}"
mkdir -p "$OUT"

echo
echo "== Baseline: KSM AUS =="
sudo ./bin/densityctl disable --unmerge=true --timeout-sec 60 || true
sudo ./bin/densityctl bench --profile P1 --scale 10..80..10 --mem-mib 256 --warmup-sec 20 --out "${OUT}/baseline" --publish "docs/data/baseline.json"

echo
echo "== DENSITY: KSM AN (konservatives Tuning) =="
sudo ./bin/densityctl enable --pages-to-scan 100 --sleep-ms 20 --merge-across-nodes 0
sudo ./bin/densityctl bench --profile P1 --scale 10..80..10 --mem-mib 256 --warmup-sec 20 --out "${OUT}/density" --publish "docs/data/density.json"

echo
echo "== Tipp: GitHub Pages =="
echo "Wenn du eine Seite willst, die automatisch Daten lädt:"
echo "  cp \"${OUT}/density\"/bench_p1_*.json docs/data/benchmarks.latest.json"
echo "  git add docs/data/benchmarks.latest.json && git commit -m \"publish benchmark\" && git push"

echo
echo "Fertig. Reports:"
echo "  ${OUT}/baseline/report.md"
echo "  ${OUT}/density/report.md"
