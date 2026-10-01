#!/usr/bin/env bash
# What Pail's container runs: versitygw, then pail-server, both until either
# stops. Everything they keep is under /data.
#
#   /data/storage   versitygw's files: every pail
#   /data/pail      pail-server's local disk
#   /data/token     the installation's token, made on the first start
#
# Pail's settings are the container's environment. A PAIL_TOKEN given there
# is used instead of the one in /data/token. A PAIL_S3_ENDPOINT given there
# is where Pail keeps everything instead: versitygw isn't started, and
# /data/storage isn't used.
set -euo pipefail

mkdir -p /data/pail
secret() { od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'; }
umask 077
[ -s /data/token ] || secret 32 > /data/token
umask 022

export PAIL_TOKEN=${PAIL_TOKEN:-$(cat /data/token)}
export PAIL_DATA_DIR=${PAIL_DATA_DIR:-/data/pail}
export PAIL_RUNTIME=${PAIL_RUNTIME:-container}

if [ -n "${PAIL_S3_ENDPOINT:-}" ]; then
  # Storage is somewhere else, so Pail is all this container runs. It takes
  # the container's signals itself.
  exec pail-server
fi

mkdir -p /data/storage
umask 077
[ -s /data/storage-secret ] || secret 24 > /data/storage-secret
umask 022
export PAIL_S3_ENDPOINT=http://127.0.0.1:7070
export PAIL_S3_ACCESS_KEY=pail
PAIL_S3_SECRET_KEY=$(cat /data/storage-secret)
export PAIL_S3_SECRET_KEY

# Storage listens only inside this container.
ROOT_ACCESS_KEY=$PAIL_S3_ACCESS_KEY ROOT_SECRET_KEY=$PAIL_S3_SECRET_KEY \
  versitygw --port 127.0.0.1:7070 posix /data/storage &
storage=$!
for _ in $(seq 1 100); do
  (exec 3<>/dev/tcp/127.0.0.1/7070) 2>/dev/null && break
  kill -0 "$storage" 2>/dev/null || { echo "pail: versitygw didn't start" >&2; exit 1; }
  sleep 0.1
done

pail-server &
server=$!
# Stopping the container stops Pail first, which lets running deploys finish
# and takes down the containers it started.
trap 'kill -TERM "$server" 2>/dev/null' TERM INT
status=0
wait -n "$storage" "$server" || status=$?
if kill -0 "$server" 2>/dev/null; then
  # A signal, or storage went: either way Pail stops next, and how it ends
  # is how the container does.
  kill -TERM "$server" 2>/dev/null || true
  status=0
  wait "$server" || status=$?
fi
kill "$storage" 2>/dev/null || true
exit "$status"
