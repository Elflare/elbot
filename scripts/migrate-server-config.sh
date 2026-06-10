#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/migrate-server-config.sh <old-elbot-repo>

Copy ElBot server config/data from the old repo layout to Linux XDG paths.
This script does not copy .env and does not delete the old repo.

Destination:
  config: ${XDG_CONFIG_HOME:-$HOME/.config}/elbot
  data:   ${XDG_DATA_HOME:-$HOME/.local/share}/elbot

Copied from old repo:
  config/app.toml
  config/providers.toml
  config/state.toml
  config/SOUL.md
  config/memories.toml
  config/plugins/
  skills/
  data/elbot_sessions.db
  data/cron_sandbox/
EOF
}

if [[ ${1:-} == "-h" || ${1:-} == "--help" ]]; then
  usage
  exit 0
fi

old_root=${1:-}
if [[ -z "$old_root" ]]; then
  usage >&2
  exit 2
fi

old_root=${old_root%/}
if [[ ! -d "$old_root" ]]; then
  echo "old repo not found: $old_root" >&2
  exit 1
fi

old_config="$old_root/config"
if [[ ! -f "$old_config/app.toml" ]]; then
  echo "old config not found: $old_config/app.toml" >&2
  exit 1
fi

config_home=${XDG_CONFIG_HOME:-$HOME/.config}
data_home=${XDG_DATA_HOME:-$HOME/.local/share}
config_dir="$config_home/elbot"
data_dir="$data_home/elbot"

mkdir -p "$config_dir" "$data_dir"

copy_file() {
  local src=$1
  local dst=$2
  if [[ -f "$src" ]]; then
    install -D -m 0644 "$src" "$dst"
    echo "copied $src -> $dst"
  fi
}

copy_dir() {
  local src=$1
  local dst=$2
  if [[ -d "$src" ]]; then
    mkdir -p "$dst"
    cp -a "$src/." "$dst/"
    echo "copied $src/ -> $dst/"
  fi
}

copy_file "$old_config/app.toml" "$config_dir/app.toml"
copy_file "$old_config/providers.toml" "$config_dir/providers.toml"
copy_file "$old_config/state.toml" "$config_dir/state.toml"
copy_file "$old_config/SOUL.md" "$config_dir/SOUL.md"
copy_file "$old_config/memories.toml" "$config_dir/memories.toml"
copy_dir "$old_config/plugins" "$config_dir/plugins"
copy_dir "$old_root/skills" "$data_dir/skills"
copy_file "$old_root/data/elbot_sessions.db" "$data_dir/elbot_sessions.db"
copy_dir "$old_root/data/cron_sandbox" "$data_dir/cron_sandbox"

tmp_app=$(mktemp)
awk -v sqlite_path="$data_dir/elbot_sessions.db" '
  BEGIN { replaced = 0; in_storage = 0 }
  /^[[:space:]]*\[/ { in_storage = ($0 == "[storage]") }
  /^[[:space:]]*sqlite_path[[:space:]]*=/ {
    print "sqlite_path = \"" sqlite_path "\""
    replaced = 1
    next
  }
  { print }
  END {
    if (!replaced) {
      if (!in_storage) {
        print ""
        print "[storage]"
      }
      print "sqlite_path = \"" sqlite_path "\""
    }
  }
' "$config_dir/app.toml" > "$tmp_app"
cat "$tmp_app" > "$config_dir/app.toml"
rm -f "$tmp_app"

echo
echo "Migration complete."
echo "Config: $config_dir/app.toml"
echo "Data:   $data_dir"
echo
echo "Next steps:"
echo "  1. Ensure provider API keys are available as environment variables."
echo "  2. Run: elbot --config '$config_dir/app.toml'"
echo "     or rely on the default XDG config lookup."
