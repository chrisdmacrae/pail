#!/usr/bin/env bash
# Runs Pail on this machine in a container, with Docker or Podman.
#
#   bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
#
# It starts one container, from one image, that holds:
#
#   versitygw     the storage Pail keeps everything in, unless
#                 PAIL_S3_ENDPOINT says to keep it somewhere else
#   pail-server   Pail itself
#
# and gives it the engine's socket, so that Pail can run builds, containers
# and functions as containers beside its own. There are no microVMs here:
# what runs is kept apart by the engine alone. That suits a machine you
# develop on; for a server, see the Proxmox installer.
#
# Run it again to move to the newest release, or to change a setting: it
# replaces the container and keeps the data. To stop and remove Pail:
#
#   bash pail-container.sh down
#
# Settings are environment variables; anything you leave out has a default,
# shown before the script changes anything:
#
#   PAIL_ENGINE       docker or podman                 (whichever is running)
#   PAIL_NAME         names the container, its network
#                     and its volume                   (pail)
#   PAIL_PORT         the port Pail answers on         (8080)
#   PAIL_BIND         the address the port is on       (127.0.0.1)
#   PAIL_BASE_DOMAIN  the domain pails get names under (localhost)
#   PAIL_TLS          "on" for HTTPS as well, with
#                     PAIL_HTTPS_PORT                  (off, 8443)
#   PAIL_VERSION      a release tag such as v0.1.0     (latest)
#   PAIL_IMAGE        where the image comes from       (ghcr.io/chrisdmacrae/pail)
#   PAIL_SOCKET       the engine's socket, as the
#                     engine itself sees it            (found by asking it)
#   PAIL_S3_ENDPOINT, PAIL_S3_ACCESS_KEY, PAIL_S3_SECRET_KEY
#                     another S3 store to keep everything in, with
#                     PAIL_S3_BUCKET, PAIL_S3_REGION and
#                     PAIL_S3_ADDRESSING               (versitygw, in the container)
#   YES=1             don't ask before starting
#
# Any other PAIL_ setting, such as PAIL_TOKEN or PAIL_MAX_UPLOAD_SIZE, is
# handed to Pail as it is.
set -euo pipefail

PAIL_NAME=${PAIL_NAME:-pail}
PAIL_PORT=${PAIL_PORT:-8080}
PAIL_HTTPS_PORT=${PAIL_HTTPS_PORT:-8443}
PAIL_BIND=${PAIL_BIND:-127.0.0.1}
PAIL_BASE_DOMAIN=${PAIL_BASE_DOMAIN:-localhost}
PAIL_TLS=${PAIL_TLS:-off}
PAIL_VERSION=${PAIL_VERSION:-latest}
PAIL_IMAGE=${PAIL_IMAGE:-ghcr.io/chrisdmacrae/pail}
ACTION=${1:-up}

say() { printf '\n==> %s\n' "$*"; }
die() { printf '\npail-container: %s\n' "$*" >&2; exit 1; }

case "$ACTION" in up | down) ;; *) die "use \"up\" to run Pail (the default) or \"down\" to stop and remove it." ;; esac
case "$PAIL_TLS" in on | off) ;; *) die "PAIL_TLS is \"on\" or \"off\"." ;; esac
STORAGE="versitygw, in the container"
if [ -n "${PAIL_S3_ENDPOINT:-}" ]; then
  [ -n "${PAIL_S3_ACCESS_KEY:-}" ] && [ -n "${PAIL_S3_SECRET_KEY:-}" ] ||
    die "with PAIL_S3_ENDPOINT, say the store's keys too: PAIL_S3_ACCESS_KEY and PAIL_S3_SECRET_KEY."
  STORAGE="the bucket ${PAIL_S3_BUCKET:-pail} at $PAIL_S3_ENDPOINT"
elif [ -n "${PAIL_S3_ACCESS_KEY:-}${PAIL_S3_SECRET_KEY:-}${PAIL_S3_BUCKET:-}${PAIL_S3_REGION:-}${PAIL_S3_ADDRESSING:-}" ]; then
  die "PAIL_S3_ACCESS_KEY and the other PAIL_S3_ settings are for a store of your own. Say where it is with PAIL_S3_ENDPOINT."
fi

# -------------------------------------------------------------- the engine

# kind says which engine a command talks to. The command's name doesn't:
# "docker" may be Podman under another name, or Docker's own command pointed
# at Podman. Either way the engine says so itself.
kind() {
  case "$("$1" version 2>/dev/null || true)" in *[Pp]odman*) echo podman ;; *) echo docker ;; esac
}

ENGINE=
if [ -n "${PAIL_ENGINE:-}" ]; then
  command -v "$PAIL_ENGINE" >/dev/null || die "PAIL_ENGINE is $PAIL_ENGINE, and there is no such command here."
  ENGINE=$PAIL_ENGINE
else
  # The first one that is running; failing that, the first one installed.
  for candidate in docker podman; do
    command -v "$candidate" >/dev/null || continue
    if "$candidate" info >/dev/null 2>&1; then ENGINE=$candidate; break; fi
  done
  if [ -z "$ENGINE" ]; then
    for candidate in docker podman; do
      if command -v "$candidate" >/dev/null; then ENGINE=$candidate; break; fi
    done
  fi
fi
[ -n "$ENGINE" ] || die "Pail needs Docker or Podman, and neither is installed. Get one from https://docs.docker.com/get-docker/ or https://podman.io."
if ! "$ENGINE" info >/dev/null 2>&1; then
  if [ "$ENGINE" = podman ] && [ "$(uname -s)" != Linux ]; then
    die "Podman is installed but its machine isn't running. Start it with: podman machine start"
  fi
  die "$ENGINE is installed but isn't running, or this user can't reach it. \"$ENGINE info\" says why."
fi
KIND=$(kind "$ENGINE")

# ------------------------------------------------------------------- down

# Pail's own container first, so it can stop what it started; then whatever
# it didn't get to.
remove_containers() {
  if "$ENGINE" container inspect "$PAIL_NAME" >/dev/null 2>&1; then
    "$ENGINE" stop -t 60 "$PAIL_NAME" >/dev/null 2>&1 || true
    "$ENGINE" rm -f "$PAIL_NAME" >/dev/null
  fi
  left=$("$ENGINE" ps -aq --filter "label=sh.pail.instance=$PAIL_NAME")
  # shellcheck disable=SC2086
  [ -z "$left" ] || "$ENGINE" rm -f $left >/dev/null
}

if [ "$ACTION" = down ]; then
  say "Stopping Pail"
  remove_containers
  "$ENGINE" network rm "$PAIL_NAME" >/dev/null 2>&1 || true
  cat <<DONE

Pail is stopped and its containers are gone. Everything it knows is still in
the volume $PAIL_NAME-data; run this script again and it carries on from there.

To delete that too, and every pail with it:

  $ENGINE volume rm $PAIL_NAME-data
  $ENGINE volume ls -q --filter label=sh.pail.instance=$PAIL_NAME | xargs $ENGINE volume rm

DONE
  exit 0
fi

# -------------------------------------------------------------- the socket

# Pail talks to the engine over its socket, mounted into Pail's container.
# The path is the one the engine sees: where the engine runs in a virtual
# machine, as on a Mac, that is a path inside it, not one on this machine.
SOCKET=${PAIL_SOCKET:-}
if [ -z "$SOCKET" ] && [ "$KIND" = podman ]; then
  # Podman says where its socket is; Docker's command, asking Podman, can't.
  SOCKET=$("$ENGINE" info --format '{{.Host.RemoteSocket.Path}}' 2>/dev/null) ||
    die "this $ENGINE command talks to Podman but can't say where Podman's socket is. Say where with PAIL_SOCKET, or use Podman's own command with PAIL_ENGINE=podman."
  SOCKET=${SOCKET#unix://}
  if [ "$("$ENGINE" info --format '{{.Host.ServiceIsRemote}}')" != true ] && [ ! -S "$SOCKET" ]; then
    # On Linux, Podman has no socket until it is asked for one.
    say "Turning on Podman's socket"
    if [ "$(id -u)" = 0 ]; then
      systemctl enable --now podman.socket >/dev/null 2>&1 || true
    else
      systemctl --user enable --now podman.socket >/dev/null 2>&1 || true
    fi
    [ -S "$SOCKET" ] || die "Podman's socket isn't at $SOCKET. Turn it on with: systemctl --user enable --now podman.socket"
  fi
fi
if [ -z "$SOCKET" ]; then
  SOCKET=/var/run/docker.sock
  host=$("$ENGINE" context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)
  system=$("$ENGINE" info --format '{{.OperatingSystem}}' 2>/dev/null || true)
  # Docker Desktop, and Docker in any other virtual machine, answer at the
  # usual path inside it. Only Docker on Linux itself can be somewhere else,
  # as it is when it runs without root.
  if [ "$(uname -s)" = Linux ] && [ "$system" != "Docker Desktop" ]; then
    case "$host" in unix://*) SOCKET=${host#unix://} ;; esac
  fi
fi

if [ "$PAIL_VERSION" = latest ]; then IMAGE=$PAIL_IMAGE; else IMAGE=$PAIL_IMAGE:$PAIL_VERSION; fi
SCHEME=http
SHOWN_PORT=$PAIL_PORT
if [ "$PAIL_TLS" = on ]; then SCHEME=https SHOWN_PORT=$PAIL_HTTPS_PORT; fi
URL=$SCHEME://$PAIL_BASE_DOMAIN
case "$SCHEME:$SHOWN_PORT" in http:80 | https:443) ;; *) URL=$URL:$SHOWN_PORT ;; esac

cat <<SUMMARY

Pail will run in a container on this machine:

  Engine        $KIND, through $SOCKET
  Container     $PAIL_NAME, from $IMAGE, starts with the engine
  Address       $URL, on $PAIL_BIND
  Base domain   $PAIL_BASE_DOMAIN
  Storage       $STORAGE
  Data          the volume $PAIL_NAME-data
  Runs code in  containers beside its own, on the network $PAIL_NAME

SUMMARY
if "$ENGINE" container inspect "$PAIL_NAME" >/dev/null 2>&1; then
  echo "A container called $PAIL_NAME is here already. It will be replaced; its data is kept."
  echo
fi
if [ "${YES:-}" != 1 ]; then
  read -r -p "Go ahead? [y/N] " answer </dev/tty
  case "$answer" in y | Y | yes) ;; *) die "nothing was changed." ;; esac
fi

# ------------------------------------------------------------------- Pail

say "Getting $IMAGE"
if ! problem=$("$ENGINE" pull "$IMAGE" 2>&1 >/dev/null); then
  "$ENGINE" image inspect "$IMAGE" >/dev/null 2>&1 || die "couldn't get $IMAGE. Check PAIL_VERSION and PAIL_IMAGE. $ENGINE said: $problem"
  echo "Couldn't fetch $IMAGE; using the copy that is here."
fi

say "Starting Pail"
remove_containers
"$ENGINE" network inspect "$PAIL_NAME" >/dev/null 2>&1 || "$ENGINE" network create "$PAIL_NAME" >/dev/null

args=(
  run --detach --name "$PAIL_NAME" --network "$PAIL_NAME"
  --restart unless-stopped --stop-timeout 60
  --volume "$PAIL_NAME-data:/data"
  --volume "$SOCKET:/var/run/docker.sock"
  --publish "$PAIL_BIND:$PAIL_PORT:$PAIL_PORT"
  --env "PAIL_LISTEN=:$PAIL_PORT"
  --env "PAIL_BASE_DOMAIN=$PAIL_BASE_DOMAIN"
  --env "PAIL_TLS=$PAIL_TLS"
  --env "PAIL_CONTAINER_NETWORK=$PAIL_NAME"
)
if [ "$PAIL_TLS" = on ]; then
  args+=(--publish "$PAIL_BIND:$PAIL_HTTPS_PORT:$PAIL_HTTPS_PORT" --env "PAIL_LISTEN_TLS=:$PAIL_HTTPS_PORT")
fi
# Podman labels what a container may touch, and the socket isn't among it.
[ "$KIND" = podman ] && args+=(--security-opt label=disable)
# Every other PAIL_ setting goes to Pail by name, so its value stays out of
# the command line.
while IFS='=' read -r setting _; do
  case "$setting" in
    PAIL_ENGINE | PAIL_NAME | PAIL_PORT | PAIL_HTTPS_PORT | PAIL_BIND | PAIL_BASE_DOMAIN | PAIL_TLS | PAIL_VERSION | PAIL_IMAGE | PAIL_SOCKET) ;;
    PAIL_LISTEN | PAIL_LISTEN_TLS | PAIL_CONTAINER_NETWORK | PAIL_CONTAINER_SOCKET) ;;
    PAIL_*) args+=(--env "$setting") ;;
  esac
done < <(env)
"$ENGINE" "${args[@]}" "$IMAGE" >/dev/null

say "Waiting for Pail to come up"
up=0
log=
for _ in $(seq 1 120); do
  log=$("$ENGINE" logs "$PAIL_NAME" 2>&1 || true)
  case "$log" in *"pail is up"*) up=1; break ;; esac
  [ "$("$ENGINE" container inspect --format '{{.State.Running}}' "$PAIL_NAME" 2>/dev/null)" = true ] || break
  sleep 1
done
if [ "$up" != 1 ]; then
  "$ENGINE" logs --tail 30 "$PAIL_NAME" >&2 || true
  die "Pail didn't start. Its log is above."
fi
BUILDS=$(grep -E "can run containers|static files only" <<<"$log" | tail -1 | sed 's/.*msg="\{0,1\}//; s/"\{0,1\} engine=.*//; s/"$//' || true)

cat <<DONE

Pail is running at $URL

  $BUILDS

DONE
if [ -z "${PAIL_TOKEN:-}" ]; then
  cat <<DONE
1. Open $URL and give it this token. Keep it somewhere safe; it
   is the only key to this Pail:

     $("$ENGINE" exec "$PAIL_NAME" cat /data/token)

DONE
else
  cat <<DONE
1. Open $URL and give it the token you set as PAIL_TOKEN.

DONE
fi
cat <<DONE
2. Deploy something. With pail-cli, from a project's folder:

     pail login $URL
     pail up

   Each pail gets a name under $PAIL_BASE_DOMAIN, like $SCHEME://hello.$PAIL_BASE_DOMAIN${URL#"$SCHEME://$PAIL_BASE_DOMAIN"}.

Log:     $ENGINE logs -f $PAIL_NAME
Token:   $ENGINE exec $PAIL_NAME cat /data/token
Update:  run this script again
Stop:    run this script with "down"
DONE
