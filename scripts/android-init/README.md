# Android init.rc auto-start (Vector A)

Boot-trigger that auto-mounts the ubuntu chroot and fires its rc.local
supervisor stack on every Android boot, via an `/system/etc/init/`
init.rc service running as root in the `u:r:su:s0` SELinux context.

This is the **userdebug-Pixel-equivalent** vector. The
`scripts/termux-boot/` Termux:Boot trigger is the alternate vector
for environments where /system is non-writable (verity enforced) but
Magisk root is available; on stock-userdebug Pixels with verity
disabled + overlayfs, Termux:Boot's untrusted_app SELinux domain
cannot escalate to root via /system/xbin/su, so this init.rc vector
is required instead.

## Layout

```
scripts/
  android-init/
    start-ubuntu.rc           # init.rc service (this directory)
    README.md                 # this file
  termux-boot/                # alternate vector (Magisk-or-equivalent)
  prod-ubuntu-chroot/         # files inside the chroot (rc.local-side)
```

## What the trigger does

1. `service start-ubuntu /system/bin/sh /data/ubuntu/start.sh` — defines a
   oneshot init service that runs `/data/ubuntu/start.sh` as root.
2. `seclabel u:r:su:s0` — runs the service in the userdebug `su` SELinux
   domain, which is the only domain on a userdebug Pixel that can both
   read `/data/ubuntu/` (label `system_data_root_file`, MAC-restricted)
   and call `setenforce permissive` (which `start.sh` does as its
   first action before chrooting and execing `/etc/rc.local`).
3. `class late_start` + `disabled` + explicit `on property:sys.boot_completed=1
   start start-ubuntu` — defers the actual start until after Zygote +
   user unlock + FBE decrypt, so `/data/ubuntu/` is readable when the
   service runs.

## Verified-on caveat

`start-ubuntu.rc` was empirically validated on:

- Pixel 8a serial **41041JEKB08092**, stock Google userdebug
  Android 16, fingerprint
  `google/akita/akita:16/BP4A.260205.001/2026031700:userdebug/release-keys`.

The `u:r:su:s0` SELinux context exists on userdebug builds. **On user-build
phones (release builds), this context may not exist** — service-start
will fail at policy-load time. Re-validate before deploying on the
prod phone (172.29.222.3, scope-locked behind `Task #7 prod phone
validation`):

```sh
adb shell ls -laZ /system/xbin/su             # confirm su_exec label
adb shell ps -eZ | grep -E ':r:(su|init):'    # confirm su context exists
```

If `u:r:su:s0` is absent on the prod phone, alternative seclabels to
investigate:

- `u:r:init:s0` — always available; runs with full unconfined init
  capabilities. Risk: bypasses the `setenforce permissive` step that
  start.sh performs (init context already has higher MAC privileges).
- A custom seclabel in a sepolicy `.te` patch — out of repo scope.

## Install (test phone)

Pre-flight:

```sh
adb shell mount | grep overlay         # check if overlayfs is active
```

If overlay is **not** active (no output, or stock /system mount):

```sh
adb root
adb remount                            # disables verity + sets up overlayfs
adb reboot                              # REQUIRED — overlayfs activates here
adb wait-for-device
adb shell mount | grep overlay         # verify overlay mounted post-reboot
adb root
adb remount                            # re-arm; overlayfs scratch now writable
```

If overlay is already active, skip the above.

Push the .rc file:

```sh
adb push scripts/android-init/start-ubuntu.rc \
    /system/etc/init/start-ubuntu.rc
adb shell chmod 0644 /system/etc/init/start-ubuntu.rc
adb shell chcon u:object_r:system_file:s0 \
    /system/etc/init/start-ubuntu.rc
adb shell ls -laZ /system/etc/init/start-ubuntu.rc   # verify label
```

Activate (first boot fires the service):

```sh
adb reboot
adb wait-for-device
sleep 30                                # let late_start cohort run + start.sh settle
adb shell mount | grep ubuntu | wc -l   # → > 0 when chroot mounted
adb shell pgrep -af '/data/ubuntu/start.sh'  # may be empty post-exit (oneshot)
adb shell pidof ffstream                # → non-empty when chroot rc.local fired
adb shell ss -tnlp | grep -E ':(3593|3594|1935)'  # supervisor LISTEN
```

## Rollback

If `/data/ubuntu/start.sh` starts misbehaving (e.g., bricking a
service or wedging boot), pull the .rc file:

```sh
adb root && adb remount
adb shell rm /system/etc/init/start-ubuntu.rc
adb reboot
```

The chroot mount + rc.local fire is suppressed on the next boot;
the operator can re-run `/data/ubuntu/start.sh` by hand to
diagnose, then re-push the .rc file once the chroot side is fixed.

## Rationale (why this vector, and not Termux:Boot)

The Termux:Boot trigger (scripts/termux-boot/start-ubuntu) fires
boot scripts in the **untrusted_app SELinux context** (`u:r:untrusted_app_*`).
On stock-userdebug Pixels:

- ✗ Cannot read `/data/ubuntu/` (label `system_data_root_file`,
  MAC-restricted to init/system_server/etc).
- ✗ Cannot exec `/system/xbin/su` (label `su_exec`, MAC restricted to
  shell + su contexts; untrusted_app explicitly excluded).
- ✗ Cannot call `setenforce permissive` (requires init or su domain).

The init.rc vector runs from the init context (root + `seclabel
u:r:su:s0`), which has all three privileges. This is the canonical
"chroot-on-boot for userdebug Android" pattern documented in the
AOSP source tree + community guides.

Termux:Boot remains useful as the trigger mechanism on devices
**with Magisk root + denylist**, where Magisk's `su` daemon can be
exec'd from untrusted_app contexts. That deployment path is
out of scope for the akita test phone (no Magisk; verity-disabled
+ overlayfs is the chosen privilege model) but documented in
scripts/termux-boot/README.md for completeness.

## Reference

- AOSP init language: `system/core/init/README.md` (in-tree, current branch)
- Test-phone fingerprint at validation time: `google/akita/akita:16/BP4A.260205.001/2026031700:userdebug/release-keys`
