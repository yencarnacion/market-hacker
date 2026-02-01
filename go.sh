#!/bin/bash

set -euo pipefail

extra=()
if [[ "${1-}" == "--historic" || "${1-}" == "historic" ]]; then
  extra+=(--historic)
fi

go run ./cmd/orb -config config.yaml -watchlist watchlist.yaml "${extra[@]}"
