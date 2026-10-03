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
apt-get install -y curl ca-certificates python3 xfce4 xfce4-terminal dbus-x11 xauth fonts-noto-cjk
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
curl -fL --retry 5 "https://d1uj6qtbmh3dt5.cloudfront.net/nice-dcv-ubuntu2404-$ARCH.tgz" -o "$WORK/dcv.tgz"
tar -xzf "$WORK/dcv.tgz" -C "$WORK"
mapfile -t PACKAGES < <(find "$WORK" -type f \( -name 'nice-dcv-server_*.deb' -o -name 'nice-dcv-web-viewer_*.deb' -o -name 'nice-xdcv_*.deb' \))
[[ "${#PACKAGES[@]}" == 3 ]] || { echo 'Missing DCV packages'; exit 1; }
apt-get install -y "${PACKAGES[@]}"
usermod -aG video dcv
install -d -m 0700 /etc/awsportal-dcv /var/lib/awsportal-dcv
install -d /usr/local/libexec
install -m 0755 "$HERE/agent.py" /usr/local/libexec/awsportal-dcv-agent
install -m 0755 "$HERE/desktop.sh" /usr/local/libexec/awsportal-dcv-desktop
cat > /etc/awsportal-dcv/user.perm <<'PERM'
[permissions]
%owner% allow display keyboard mouse pointer audio-out
%any% deny file-download file-upload clipboard-copy clipboard-paste printer usb screenshot
PERM
# A dedicated local broker authenticates over verified HTTPS to the portal.
[[ ! -f /etc/dcv/dcv.conf ]] || cp -a /etc/dcv/dcv.conf /etc/dcv/dcv.conf.before-awsportal
cat > /etc/dcv/dcv.conf <<'CONF'
[security]
authentication="none"
auth-token-verifier="http://127.0.0.1:8444"
[connectivity]
web-port=8443
enable-quic-frontend=false
[session-management]
create-session=false
CONF
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
[[ ! -f /etc/awsportal-dcv/config.json ]] || systemctl enable --now awsportal-dcv-agent
echo 'DCV installed. Configure /etc/awsportal-dcv/config.json (root:root, 0600) and start awsportal-dcv-agent.'
