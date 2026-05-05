#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
deploy_script="$script_dir/deploy.sh"
readme="$script_dir/README.md"
rc_local_fragment="$script_dir/rc.local.fragment"
legacy_rc_local_snippet="$script_dir/../rc.local.snippet"

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
	fail "README.md must document the rc.local camera supervisor path"
fi

if ! grep -q "rc.local" "$deploy_script"; then
	fail "deploy.sh must describe rc.local ownership"
fi
if ! grep -q "rc.local" "$readme"; then
	fail "README.md must describe rc.local ownership"
fi

for rc_local in "$rc_local_fragment" "$legacy_rc_local_snippet"; do
	if ! grep -q "/usr/local/bin/loop-run-ffstream.sh" "$rc_local"; then
		fail "$rc_local must start the mediamtx ffstream supervisor"
	fi
	if ! grep -q "/usr/local/bin/loop-run-ffstream-camera.sh" "$rc_local"; then
		fail "$rc_local must start the camera ffstream supervisor"
	fi
done

if grep -Eiq "Wing ?Out|Wingout|platform_android|restart hook" "$deploy_script" "$readme"; then
	fail "deploy docs/messages must describe rc.local ownership, not Wingout/platform Android ownership"
fi
