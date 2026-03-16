#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="$ROOT_DIR/data"

mkdir -p "$DATA_DIR"

copy_if_exists() {
  local src="$1"
  local dst="$2"
  if [[ -f "$src" ]]; then
    cp "$src" "$dst"
    echo "copied: $src -> $dst"
  fi
}

if [[ $# -gt 0 ]]; then
  SOURCE_DIR="$1"
else
  SOURCE_DIR="$(pwd)"
fi

copy_if_exists "$SOURCE_DIR/locations.json" "$DATA_DIR/locations.json"
copy_if_exists "$SOURCE_DIR/ips-v4.txt" "$DATA_DIR/ips-v4.txt"
copy_if_exists "$SOURCE_DIR/ips-v6.txt" "$DATA_DIR/ips-v6.txt"

echo "done"
