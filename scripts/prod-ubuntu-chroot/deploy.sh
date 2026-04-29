#!/bin/bash
# Deploy chroot scripts to prod (root@172.29.222.3 by default).
#
# Touches only paths inside the Ubuntu chroot:
#   /usr/local/bin/run-ffstream.sh
#   /usr/local/bin/loop-run-ffstream.sh
#   /etc/mediamtx/mediamtx.yml
#   /etc/streaming.env (only when absent — non-destructive)
#
# Does NOT modify /android, /data, /system, /apex.
# Does NOT restart ffstream — operator triggers respawn manually.
set -euo pipefail
TARGET="${1:-root@172.29.222.3}"
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

scp "$SCRIPT_DIR/run-ffstream.sh" "$SCRIPT_DIR/loop-run-ffstream.sh" "$TARGET:/usr/local/bin/"
ssh "$TARGET" 'chmod +x /usr/local/bin/run-ffstream.sh /usr/local/bin/loop-run-ffstream.sh'

scp "$SCRIPT_DIR/mediamtx.yml" "$TARGET:/etc/mediamtx/"

# streaming.env: stage at /tmp; install only if /etc copy is absent (preserve operator edits).
scp "$SCRIPT_DIR/streaming.env.template" "$TARGET:/tmp/streaming.env.staged"
ssh "$TARGET" 'test -f /etc/streaming.env || cp /tmp/streaming.env.staged /etc/streaming.env'

echo "Deploy done."
echo "Restart ffstream via: ssh $TARGET 'kill \$(pidof ffstream)' (loop-run-ffstream.sh respawns within 0.1s)"
