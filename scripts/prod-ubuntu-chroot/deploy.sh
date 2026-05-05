#!/bin/bash
# Deploy chroot scripts to prod (root@172.29.222.3 by default).
#
# Touches only paths inside the Ubuntu chroot:
#   /usr/local/bin/run-ffstream.sh
#   /usr/local/bin/loop-run-ffstream.sh
#   /usr/local/bin/run-ffstream-camera.sh
#   /usr/local/bin/loop-run-ffstream-camera.sh
#   /etc/mediamtx/mediamtx.yml
#   /etc/streaming.env (only when absent — non-destructive)
#
# Does NOT modify /android, /data, /system, /apex.
# Runtime-only Android paths used by these launchers are not deploy targets:
#   /data/ubuntu/tmp/ffstream*.log
#   /data/ubuntu/tmp/loop-run-ffstream*.log
#   /data/ubuntu/tmp/ffstream-camera.intentional-end
#   /android/data/ubuntu/tmp/ffstream-camera.intentional-end
# Does NOT restart ffstream supervisors; rc.local owns startup. On prod, apply
# rc.local changes by rebooting after deploy.
set -euo pipefail
TARGET="${1:-root@172.29.222.3}"
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

scp \
	"$SCRIPT_DIR/run-ffstream.sh" \
	"$SCRIPT_DIR/loop-run-ffstream.sh" \
	"$SCRIPT_DIR/run-ffstream-camera.sh" \
	"$SCRIPT_DIR/loop-run-ffstream-camera.sh" \
	"$TARGET:/usr/local/bin/"
ssh "$TARGET" 'chmod +x /usr/local/bin/run-ffstream.sh /usr/local/bin/loop-run-ffstream.sh /usr/local/bin/run-ffstream-camera.sh /usr/local/bin/loop-run-ffstream-camera.sh'

scp "$SCRIPT_DIR/mediamtx.yml" "$TARGET:/etc/mediamtx/"

# streaming.env: stage at /tmp; install only if /etc copy is absent (preserve operator edits).
scp "$SCRIPT_DIR/streaming.env.template" "$TARGET:/tmp/streaming.env.staged"
ssh "$TARGET" 'test -f /etc/streaming.env || cp /tmp/streaming.env.staged /etc/streaming.env'

echo "Deploy done."
echo "rc.local owns mediamtx supervisor startup: /usr/local/bin/loop-run-ffstream.sh"
echo "rc.local owns camera supervisor startup: /usr/local/bin/loop-run-ffstream-camera.sh"
echo "After prod deploy, reboot to apply rc.local startup changes."
