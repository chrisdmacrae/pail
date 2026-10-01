#!/usr/bin/env bash
# Sets up Pail in a Proxmox LXC container.
#
# Run it on the Proxmox host, as root:
#
#   bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
#
# It makes an unprivileged container, gives it /dev/kvm and /dev/net/tun so
# Pail can run Firecracker microVMs, and inside it installs and starts:
#
#   versitygw     the storage Pail keeps everything in
#   pail-server   Pail itself, on ports 80 and 443
#   firecracker   and a guest kernel, for builds
#
# Settings are environment variables; anything you leave out has a default,
# shown before the script changes anything:
#
#   CTID            container ID                      (the next free one)
#   CT_HOSTNAME     container hostname                (pail)
#   STORAGE         storage for its disk              (local-lvm)
#   TEMPLATES       storage holding CT templates      (local)
#   BRIDGE          network bridge                    (vmbr0)
#   IP              "dhcp", or an address like 10.0.0.50/24
#   GATEWAY         its gateway, when IP is an address
#   CORES           CPU cores                         (4)
#   MEMORY          memory in MB                      (4096)
#   DISK            disk in GB                        (32)
#   BASE_DOMAIN     the domain pails get names under  (pail.lan)
#   PAIL_VERSION    a release tag such as v0.1.0      (latest)
#   PAIL_REPO       where releases come from          (chrisdmacrae/pail)
#   ACME_DNS_PROVIDER, ACME_DNS_TOKEN, ACME_EMAIL
#                   for Let's Encrypt certificates and custom hostnames
#   YES=1           don't ask before starting
set -euo pipefail

PAIL_REPO=${PAIL_REPO:-chrisdmacrae/pail}
PAIL_VERSION=${PAIL_VERSION:-latest}
CT_HOSTNAME=${CT_HOSTNAME:-pail}
STORAGE=${STORAGE:-local-lvm}
TEMPLATES=${TEMPLATES:-local}
BRIDGE=${BRIDGE:-vmbr0}
IP=${IP:-dhcp}
GATEWAY=${GATEWAY:-}
CORES=${CORES:-4}
MEMORY=${MEMORY:-4096}
DISK=${DISK:-32}
BASE_DOMAIN=${BASE_DOMAIN:-pail.lan}
ACME_DNS_PROVIDER=${ACME_DNS_PROVIDER:-}
ACME_DNS_TOKEN=${ACME_DNS_TOKEN:-}
ACME_EMAIL=${ACME_EMAIL:-}

# What Pail runs microVMs with. The kernel is one of Firecracker's own builds.
VERSITYGW_VERSION=${VERSITYGW_VERSION:-v1.8.0}
FC_VERSION=${FC_VERSION:-v1.17.0}
FC_KERNEL_URL=${FC_KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/ARCH/vmlinux-6.1.155}

say() { printf '\n==> %s\n' "$*"; }
die() { printf '\npail-proxmox: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- the host

[ "$(id -u)" = 0 ] || die "run this as root on the Proxmox host."
command -v pct >/dev/null && command -v pveam >/dev/null || die "this isn't a Proxmox host: pct and pveam aren't here."
[ -e /dev/kvm ] || die "this host has no /dev/kvm. Turn on virtualization in its firmware; Pail needs it for builds."
if [ -n "$ACME_DNS_PROVIDER$ACME_DNS_TOKEN" ] && { [ -z "$ACME_DNS_PROVIDER" ] || [ -z "$ACME_DNS_TOKEN" ]; }; then
  die "ACME_DNS_PROVIDER and ACME_DNS_TOKEN go together. Set both, or neither."
fi
if [ "$IP" != dhcp ] && [ -z "$GATEWAY" ]; then
  die "with IP=$IP, say the gateway too: GATEWAY=10.0.0.1"
fi

CTID=${CTID:-$(pvesh get /cluster/nextid)}
pct status "$CTID" >/dev/null 2>&1 && die "container $CTID already exists. Pick another with CTID=..."

case "$(dpkg --print-architecture)" in
  amd64) GOARCH=amd64 UNAME_ARCH=x86_64 ;;
  arm64) GOARCH=arm64 UNAME_ARCH=aarch64 ;;
  *) die "Pail's microVMs run on amd64 and arm64 only." ;;
esac

if [ "$PAIL_VERSION" = latest ]; then
  RELEASE="https://github.com/$PAIL_REPO/releases/latest/download"
else
  RELEASE="https://github.com/$PAIL_REPO/releases/download/$PAIL_VERSION"
fi

cat <<SUMMARY

Pail will be set up in a new container:

  Container     $CTID ($CT_HOSTNAME), unprivileged, starts on boot
  Resources     $CORES cores, ${MEMORY}MB memory, ${DISK}GB disk on $STORAGE
  Network       $BRIDGE, ${IP}${GATEWAY:+ via $GATEWAY}
  Base domain   $BASE_DOMAIN
  Certificates  $([ -n "$ACME_DNS_PROVIDER" ] && echo "Let's Encrypt through $ACME_DNS_PROVIDER" || echo "Pail's own authority (each device trusts it once)")
  Pail          $PAIL_VERSION from github.com/$PAIL_REPO

SUMMARY
if [ "${YES:-}" != 1 ]; then
  read -r -p "Go ahead? [y/N] " answer </dev/tty
  case "$answer" in y | Y | yes) ;; *) die "nothing was changed." ;; esac
fi

say "Checking the release is there"
curl -fsSLI "$RELEASE/pail-server_linux_$GOARCH.tar.gz" >/dev/null ||
  die "no pail-server at $RELEASE. Check PAIL_VERSION and PAIL_REPO."

say "Getting a Debian template"
pveam update >/dev/null
TEMPLATE=$(pveam available --section system | awk '{print $2}' | grep -E '^debian-12-standard_.*_'"$GOARCH"'\.tar\.(zst|gz|xz)$' | sort -V | tail -1)
[ -n "$TEMPLATE" ] || die "couldn't find a Debian 12 template for $GOARCH."
pveam list "$TEMPLATES" | grep -q "$TEMPLATE" || pveam download "$TEMPLATES" "$TEMPLATE"

say "Making container $CTID"
NET="name=eth0,bridge=$BRIDGE,ip=$IP"
[ "$IP" != dhcp ] && NET="$NET,gw=$GATEWAY"
pct create "$CTID" "$TEMPLATES:vztmpl/$TEMPLATE" \
  --hostname "$CT_HOSTNAME" --cores "$CORES" --memory "$MEMORY" --swap 512 \
  --rootfs "$STORAGE:$DISK" --net0 "$NET" \
  --unprivileged 1 --features nesting=1 --onboot 1 \
  --description "Pail: https://github.com/$PAIL_REPO"

say "Giving it /dev/kvm and /dev/net/tun"
# Pail runs each build in a Firecracker microVM. That needs KVM, and a tap
# device per microVM for its network.
modprobe tun 2>/dev/null || true
echo tun > /etc/modules-load.d/pail.conf
if pct set "$CTID" --dev0 /dev/kvm,mode=0666 --dev1 /dev/net/tun,mode=0666 2>/dev/null; then
  : # Proxmox 8.1 and later pass devices through by themselves
else
  # Older Proxmox: allow the devices and bind them in by hand. An unprivileged
  # container's root isn't the host's, so the host's nodes must be open to it.
  cat >> "/etc/pve/lxc/$CTID.conf" <<LXC
lxc.cgroup2.devices.allow: c 10:232 rwm
lxc.mount.entry: /dev/kvm dev/kvm none bind,optional,create=file
lxc.cgroup2.devices.allow: c 10:200 rwm
lxc.mount.entry: /dev/net/tun dev/net/tun none bind,optional,create=file
LXC
  echo 'KERNEL=="kvm", MODE="0666"' > /etc/udev/rules.d/99-pail-kvm.rules
  udevadm control --reload-rules && udevadm trigger --name-match=kvm
  chmod 0666 /dev/kvm /dev/net/tun
fi

say "Starting it"
pct start "$CTID"
# Wait for the network: everything after this downloads something.
for _ in $(seq 1 60); do
  pct exec "$CTID" -- sh -c 'getent hosts github.com >/dev/null 2>&1' && break
  sleep 1
done
pct exec "$CTID" -- sh -c 'getent hosts github.com >/dev/null' || die "container $CTID has no network. Check BRIDGE, IP and GATEWAY."

# ----------------------------------------------------------- the container

say "Installing Pail inside the container"
PAIL_TOKEN=$(openssl rand -hex 32)
S3_SECRET=$(openssl rand -hex 24)
KERNEL_URL=${FC_KERNEL_URL/ARCH/$UNAME_ARCH}

pct exec "$CTID" -- env \
  RELEASE="$RELEASE" PAIL_REPO="$PAIL_REPO" GOARCH="$GOARCH" UNAME_ARCH="$UNAME_ARCH" \
  VERSITYGW_VERSION="$VERSITYGW_VERSION" FC_VERSION="$FC_VERSION" KERNEL_URL="$KERNEL_URL" \
  PAIL_TOKEN="$PAIL_TOKEN" S3_SECRET="$S3_SECRET" BASE_DOMAIN="$BASE_DOMAIN" \
  ACME_DNS_PROVIDER="$ACME_DNS_PROVIDER" ACME_DNS_TOKEN="$ACME_DNS_TOKEN" ACME_EMAIL="$ACME_EMAIL" \
  bash -s <<'INSIDE'
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

apt-get update -qq
apt-get install -y -qq curl ca-certificates e2fsprogs iptables iproute2 >/dev/null

mkdir -p /etc/pail /var/lib/pail /var/lib/versitygw /usr/local/bin
tmp=$(mktemp -d)
cd "$tmp"

echo "  pail-server"
curl -fsSL "$RELEASE/pail-server_linux_$GOARCH.tar.gz" | tar -xz
install -m 0755 pail-server /usr/local/bin/pail-server

echo "  versitygw $VERSITYGW_VERSION"
curl -fsSL "https://github.com/versity/versitygw/releases/download/$VERSITYGW_VERSION/versitygw_${VERSITYGW_VERSION}_Linux_$( [ "$GOARCH" = amd64 ] && echo x86_64 || echo arm64 ).tar.gz" | tar -xz
install -m 0755 "$(find . -type f -name versitygw | head -1)" /usr/local/bin/versitygw

echo "  firecracker $FC_VERSION"
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-$UNAME_ARCH.tgz" | tar -xz
install -m 0755 "release-$FC_VERSION-$UNAME_ARCH/firecracker-$FC_VERSION-$UNAME_ARCH" /usr/local/bin/firecracker

echo "  guest kernel"
curl -fsSL -o /var/lib/pail/vmlinux "$KERNEL_URL"
cd / && rm -rf "$tmp"

# Storage: versitygw, listening only inside the container.
cat > /etc/pail/versitygw.env <<ENV
ROOT_ACCESS_KEY=pail
ROOT_SECRET_KEY=$S3_SECRET
ENV
cat > /etc/systemd/system/versitygw.service <<'UNIT'
[Unit]
Description=versitygw, the storage Pail keeps everything in
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/pail/versitygw.env
ExecStart=/usr/local/bin/versitygw --port 127.0.0.1:7070 posix /var/lib/versitygw
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT

# Pail. Everything it can be told is in this one file.
cat > /etc/pail/pail.env <<ENV
# Pail's settings. After changing any: systemctl restart pail
PAIL_TOKEN=$PAIL_TOKEN
PAIL_BASE_DOMAIN=$BASE_DOMAIN
PAIL_S3_ENDPOINT=http://127.0.0.1:7070
PAIL_S3_ACCESS_KEY=pail
PAIL_S3_SECRET_KEY=$S3_SECRET
PAIL_DATA_DIR=/var/lib/pail
PAIL_FIRECRACKER=/usr/local/bin/firecracker
PAIL_KERNEL=/var/lib/pail/vmlinux
ENV
if [ -n "$ACME_DNS_PROVIDER" ]; then
  cat >> /etc/pail/pail.env <<ENV
PAIL_ACME_DNS_PROVIDER=$ACME_DNS_PROVIDER
PAIL_ACME_DNS_TOKEN=$ACME_DNS_TOKEN
ENV
  [ -n "$ACME_EMAIL" ] && echo "PAIL_ACME_EMAIL=$ACME_EMAIL" >> /etc/pail/pail.env
fi
chmod 600 /etc/pail/pail.env /etc/pail/versitygw.env

# Pail runs as root: each microVM gets a network device and firewall rules
# of its own, and only root in the container may make those.
cat > /etc/systemd/system/pail.service <<'UNIT'
[Unit]
Description=Pail
After=versitygw.service network-online.target
Requires=versitygw.service
Wants=network-online.target

[Service]
EnvironmentFile=/etc/pail/pail.env
ExecStart=/usr/local/bin/pail-server
Restart=always
RestartSec=2
# Let running deploys finish when stopping.
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
UNIT

# One command to move to another release: the newest, or the one named, as
# in: pail-update v0.2.0
cat > /usr/local/bin/pail-update <<UPDATE
#!/bin/sh
set -e
repo="$PAIL_REPO"
arch="$GOARCH"
UPDATE
cat >> /usr/local/bin/pail-update <<'UPDATE'
case "${1:-latest}" in
  latest) url="https://github.com/$repo/releases/latest/download" ;;
  *) url="https://github.com/$repo/releases/download/$1" ;;
esac
tmp=$(mktemp -d)
curl -fsSL "$url/pail-server_linux_$arch.tar.gz" | tar -xz -C "$tmp"
install -m 0755 "$tmp/pail-server" /usr/local/bin/pail-server
rm -rf "$tmp"
systemctl restart pail
echo "Pail was replaced with the ${1:-latest} release and restarted. Its log: journalctl -u pail -f"
UPDATE
chmod 0755 /usr/local/bin/pail-update

systemctl daemon-reload
systemctl enable --now versitygw.service pail.service >/dev/null 2>&1
INSIDE

# ----------------------------------------------------------------- the end

say "Waiting for Pail to come up"
up=0
for _ in $(seq 1 90); do
  if pct exec "$CTID" -- sh -c 'journalctl -u pail --no-pager 2>/dev/null | grep -q "pail is up"'; then up=1; break; fi
  pct exec "$CTID" -- systemctl is-failed --quiet pail && break
  sleep 2
done
ADDR=$(pct exec "$CTID" -- sh -c "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1" | head -1)
BUILDS=$(pct exec "$CTID" -- sh -c 'journalctl -u pail --no-pager | grep -E "can run microVMs|static files only" | tail -1 | sed "s/.*msg=//"' || true)

if [ "$up" != 1 ]; then
  pct exec "$CTID" -- journalctl -u pail -n 30 --no-pager >&2 || true
  die "Pail didn't start in container $CTID. Its log is above; its settings are in /etc/pail/pail.env there."
fi

cat <<DONE

Pail is running in container $CTID at $ADDR.

  $BUILDS

1. Point names at it. On your home resolver, send $BASE_DOMAIN and
   everything under it to $ADDR. For dnsmasq or Pi-hole:

     address=/$BASE_DOMAIN/$ADDR

2. Open https://$BASE_DOMAIN and give it this token. Keep it somewhere
   safe; it is the only key to this Pail:

     $PAIL_TOKEN

DONE
if [ -z "$ACME_DNS_PROVIDER" ]; then
  cat <<DONE
3. This Pail signs its own certificates, so each device trusts it once.
   The root certificate is at http://$BASE_DOMAIN/ca.crt, and the page at
   https://$BASE_DOMAIN/trust has the steps for each kind of device.

DONE
fi
cat <<DONE
From a terminal:  PAIL_TOKEN=<the token> pail login https://$BASE_DOMAIN

Settings:  pct exec $CTID -- nano /etc/pail/pail.env   then   pct exec $CTID -- systemctl restart pail
Log:       pct exec $CTID -- journalctl -u pail -f
Update:    pct exec $CTID -- pail-update
DONE
