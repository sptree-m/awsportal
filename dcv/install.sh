#!/usr/bin/env bash
set -euo pipefail
[[ "$(id -u)" == 0 ]] || { echo 'Run as root'; exit 1; }
HERE="$(cd "$(dirname "$0")" && pwd)"
export DEBIAN_FRONTEND=noninteractive
source /etc/os-release
[[ "$ID" == ubuntu && "$VERSION_ID" == 24.04 ]] || {
  echo 'Automatic installer supports Ubuntu 24.04 only'; exit 1;
}
case "$(dpkg --print-architecture)" in
  arm64) ARCH=aarch64;;
  amd64) ARCH=x86_64;;
  *) echo 'Unsupported architecture'; exit 1;;
esac
apt-get update
apt-get install -y curl ca-certificates python3 xfce4 xfce4-terminal dbus-x11 xauth fonts-noto-cjk iptables iptables-persistent
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
curl -fL --retry 5 "https://d1uj6qtbmh3dt5.cloudfront.net/nice-dcv-ubuntu2404-$ARCH.tgz" -o "$WORK/dcv.tgz"
tar -xzf "$WORK/dcv.tgz" -C "$WORK"
mapfile -t PACKAGES < <(find "$WORK" -type f \( -name 'nice-dcv-server_*.deb' -o -name 'nice-xdcv_*.deb' \))
[[ "${#PACKAGES[@]}" == 2 ]] || { echo 'Missing DCV packages'; exit 1; }
apt-get install -y "${PACKAGES[@]}"
# Removing the hosted viewer alone is insufficient; reject browser Origins below.
if [[ "$(dpkg-query -W -f='${db:Status-Status}' nice-dcv-web-viewer 2>/dev/null || true)" == installed ]]; then
  apt-get purge -y nice-dcv-web-viewer
fi
usermod -aG video dcv
# EC2 UserData and instance-role credentials must not be readable by desktop users.
# IMDSv2 alone does not distinguish a root process from an unprivileged local user.
for TOOL in iptables ip6tables; do
  if [[ "$TOOL" == iptables ]]; then TARGET=169.254.169.254/32; else TARGET=fd00:ec2::254/128; fi
  "$TOOL" -N AWSPORTAL_IMDS 2>/dev/null || "$TOOL" -L AWSPORTAL_IMDS >/dev/null
  "$TOOL" -F AWSPORTAL_IMDS
  "$TOOL" -A AWSPORTAL_IMDS -m owner --uid-owner 0 -j RETURN
  "$TOOL" -A AWSPORTAL_IMDS -m owner --uid-owner "$(id -u dcv)" -j RETURN
  "$TOOL" -A AWSPORTAL_IMDS -j REJECT
  "$TOOL" -C OUTPUT -d "$TARGET" -j AWSPORTAL_IMDS 2>/dev/null || \
    "$TOOL" -I OUTPUT 1 -d "$TARGET" -j AWSPORTAL_IMDS
done
netfilter-persistent save
install -d -m 0700 /etc/awsportal-dcv /var/lib/awsportal-dcv
install -d /usr/local/libexec
install -m 0755 "$HERE/agent.py" /usr/local/libexec/awsportal-dcv-agent
install -m 0755 "$HERE/desktop.sh" /usr/local/libexec/awsportal-dcv-desktop
install -d -o root -g root -m 0755 /etc/dcv/awsportal-policy
cat > /etc/dcv/awsportal-policy/enforced.perm <<'PERM'
[permissions]
%any% deny audio-in clipboard-copy clipboard-paste file-download file-upload screenshot printer usb smartcard webcam gamepad stylus touch keyboard-sas webauthn-redirection extensions-client extensions-server unsupervised-access
PERM
# A dedicated local broker authenticates over verified HTTPS to the portal.
[[ ! -f /etc/dcv/dcv.conf ]] || cp -a /etc/dcv/dcv.conf /etc/dcv/dcv.conf.before-awsportal
cat > /etc/dcv/dcv.conf <<'CONF'
[security]
authentication="none"
auth-token-verifier="http://127.0.0.1:8444"
allowed-ws-origin-regex="^$"
[connectivity]
web-port=8443
enable-quic-frontend=false
[session-management]
create-session=false
[session-management/defaults]
permissions-file="/etc/dcv/awsportal-policy/enforced.perm"
CONF
chown root:root /etc/dcv/dcv.conf /etc/dcv/awsportal-policy/enforced.perm
chmod 0644 /etc/dcv/dcv.conf /etc/dcv/awsportal-policy/enforced.perm
chown root:root /etc/dcv /etc/awsportal-dcv
chmod 0755 /etc/dcv
chmod 0700 /etc/awsportal-dcv
cat > /etc/systemd/system/awsportal-dcv-agent.service <<'UNIT'
[Unit]
Description=awsportal DCV account synchronization and authentication
After=network-online.target dcvserver.service
Wants=network-online.target
Requires=dcvserver.service
[Service]
Type=simple
User=root
UMask=0077
ExecStart=/usr/bin/python3 /usr/local/libexec/awsportal-dcv-agent
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable dcvserver
systemctl restart dcvserver
if [[ -f /etc/awsportal-dcv/config.json ]]; then
  systemctl enable awsportal-dcv-agent
  systemctl restart awsportal-dcv-agent
fi
echo 'DCV installed. Configure /etc/awsportal-dcv/config.json (root:root, 0600) and start awsportal-dcv-agent.'
