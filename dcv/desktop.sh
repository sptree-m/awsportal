#!/bin/sh
# DCV executes this as the session user. Keep the desktop and every child in a
# distinct measured scope; awsportal-job creates sibling measured job scopes.
uid=$(id -u)
export XDG_RUNTIME_DIR="/run/user/$uid"
unset SESSION_MANAGER DBUS_SESSION_BUS_ADDRESS
export TMPDIR="/work-local/awp-u$((uid - 200000))/tmp"
if [ ! -d "$TMPDIR" ]; then
  export TMPDIR=/tmp
fi
exec systemd-run --user --scope --quiet --unit="awsportal-desktop-$uid" \
  --property=CPUAccounting=yes --property=MemoryAccounting=yes --property=IOAccounting=yes \
  -- dbus-run-session -- startxfce4
