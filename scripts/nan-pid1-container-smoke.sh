#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE="${CONTAINER_ENGINE:-podman}"
IMAGE="${NAN_SMOKE_IMAGE:-nan-pid1-smoke:local}"
BASE_IMAGE="${NAN_SMOKE_BASE_IMAGE:-docker.io/library/alpine:3.20}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nan-pid1-smoke.XXXXXX")"
CONTAINER_NAME="nan-pid1-smoke-$$"
RUNTIME_DIR="${XDG_RUNTIME_DIR:-}"
if [[ -z "$RUNTIME_DIR" || ! -w "$RUNTIME_DIR" ]]; then
	RUNTIME_DIR="${TMPDIR:-/tmp}/nan-podman-runtime-${UID}"
fi

cleanup() {
	"$ENGINE" rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
	rm -rf "$WORK_DIR"
}
trap cleanup EXIT

mkdir -p "$RUNTIME_DIR" "$WORK_DIR/out" "$WORK_DIR/work"
chmod 700 "$RUNTIME_DIR"
export XDG_RUNTIME_DIR="$RUNTIME_DIR"

VOLUME_SUFFIX=":Z"
if [[ "$ENGINE" == "docker" ]]; then
	VOLUME_SUFFIX=""
fi

make -C "$ROOT_DIR" build

cat >"$WORK_DIR/Containerfile" <<EOF
FROM ${BASE_IMAGE}
COPY bin/nan /usr/local/bin/nan
ENTRYPOINT ["/usr/local/bin/nan"]
EOF

"$ENGINE" build -f "$WORK_DIR/Containerfile" -t "$IMAGE" "$ROOT_DIR"

"$ENGINE" run -d \
	--name "$CONTAINER_NAME" \
	-v "$WORK_DIR/out:/out${VOLUME_SUFFIX}" \
	-v "$WORK_DIR/work:/work${VOLUME_SUFFIX}" \
	"$IMAGE" \
	run \
	--run-id smoke \
	--node-id pid1 \
	--attempt-id attempt-1 \
	--output-names result \
	--work-root /work \
	--output-root /out \
	--manifest-path /out/artifacts.json \
	--termination-log-path /out/termination.json \
	--shutdown-grace-period 200ms \
	-- sh -c 'sleep 60' >/dev/null

sleep 1
"$ENGINE" kill --signal TERM "$CONTAINER_NAME" >/dev/null

set +e
exit_code="$("$ENGINE" wait "$CONTAINER_NAME")"
set -e

if [[ "$exit_code" != "143" ]]; then
	echo "FAIL: container exit code = ${exit_code}, want 143" >&2
	exit 1
fi

if [[ -e "$WORK_DIR/out/artifacts.json" ]]; then
	echo "FAIL: success manifest exists after SIGTERM" >&2
	exit 1
fi

if [[ ! -e "$WORK_DIR/out/termination.json" ]]; then
	echo "FAIL: termination summary was not written" >&2
	exit 1
fi

echo "OK: nan PID 1 container smoke passed"
