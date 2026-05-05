#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
deploy_script="$script_dir/deploy.sh"
readme="$script_dir/README.md"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

for script in \
	run-ffstream.sh \
	loop-run-ffstream.sh \
	run-ffstream-camera.sh \
	loop-run-ffstream-camera.sh
do
	if ! grep -q "$script" "$deploy_script"; then
		fail "deploy.sh must install $script"
	fi
	if ! grep -q "$script" "$readme"; then
		fail "README.md must document $script"
	fi
done

if ! grep -q "/usr/local/bin/loop-run-ffstream-camera.sh" "$readme"; then
	fail "README.md must document the Android restart-hook supervisor path"
fi
