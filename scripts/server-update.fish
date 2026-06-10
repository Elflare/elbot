#!/usr/bin/env fish

set -l repo_dir /opt/elbot/src
set -l bin_path /opt/elbot/bin/elbot
set -l service_name elbot
set -l branch main
set -l force_sync 0

function usage
    echo "Usage: scripts/server-update.fish [options]"
    echo
    echo "Options:"
    echo "  --repo DIR        Source repo dir. Default: /opt/elbot/src"
    echo "  --bin PATH        Installed binary path. Default: /opt/elbot/bin/elbot"
    echo "  --service NAME    systemd service name. Default: elbot"
    echo "  --branch NAME     Git branch. Default: main"
    echo "  --force-sync      Reset hard to origin/BRANCH and clean ignored files"
    echo "  -h, --help        Show help"
end

set -l i 1
while test $i -le (count $argv)
    switch $argv[$i]
        case --repo
            set i (math $i + 1)
            set repo_dir $argv[$i]
        case --bin
            set i (math $i + 1)
            set bin_path $argv[$i]
        case --service
            set i (math $i + 1)
            set service_name $argv[$i]
        case --branch
            set i (math $i + 1)
            set branch $argv[$i]
        case --force-sync
            set force_sync 1
        case -h --help
            usage
            exit 0
        case '*'
            echo "unknown option: $argv[$i]" >&2
            usage >&2
            exit 2
    end
    set i (math $i + 1)
end

if not test -d "$repo_dir/.git"
    echo "repo not found: $repo_dir" >&2
    exit 1
end

cd "$repo_dir"; or exit 1

echo "==> fetching origin"
git fetch origin; or exit 1

if test $force_sync -eq 1
    echo "==> force syncing to origin/$branch"
    git reset --hard "origin/$branch"; or exit 1
    git clean -fdx; or exit 1
else
    echo "==> pulling $branch"
    git checkout "$branch"; or exit 1
    git pull --ff-only origin "$branch"; or exit 1
end

set -l tmp_bin (mktemp /tmp/elbot.XXXXXX)

echo "==> building $tmp_bin"
go build -o "$tmp_bin" ./cmd/elbot; or begin
    rm -f "$tmp_bin"
    exit 1
end

echo "==> installing $bin_path"
sudo install -D -m 0755 "$tmp_bin" "$bin_path"; or begin
    rm -f "$tmp_bin"
    exit 1
end
rm -f "$tmp_bin"

echo "==> restarting $service_name"
sudo systemctl restart "$service_name"; or exit 1
sudo systemctl --no-pager --full status "$service_name"
