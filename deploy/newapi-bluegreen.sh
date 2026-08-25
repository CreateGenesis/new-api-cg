#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

BASE_DIR="${NEW_API_BLUEGREEN_DIR:-/www/dk_project/dk_app/newapi/newapi_TMrS/bluegreen}"
APP_DIR="${NEW_API_APP_DIR:-$(cd "$BASE_DIR/.." && pwd)}"
COMPOSE_FILE="${NEW_API_COMPOSE_FILE:-$APP_DIR/docker-compose.yml}"
ENV_FILE="${NEW_API_ENV_FILE:-$APP_DIR/.env}"
PROJECT_NAME="${NEW_API_PROJECT_NAME:-newapi_tmrs}"
STATE_FILE="$BASE_DIR/state.env"
RUN_LOG="$BASE_DIR/deploy.log"
LOCK_FILE="$BASE_DIR/.lock"
NGINX_SWITCH_FILE="${NEW_API_NGINX_SWITCH_FILE:-/www/server/panel/vhost/nginx/extension/ai.createsis.cn/newapi-bluegreen.conf}"
NGINX_VHOST="${NEW_API_NGINX_VHOST:-/www/server/panel/vhost/nginx/ai.createsis.cn.conf}"
NGINX_CONF="${NEW_API_NGINX_CONF:-/www/server/nginx/conf/nginx.conf}"
NGINX_BIN="${NEW_API_NGINX_BIN:-/www/server/nginx/sbin/nginx}"
LOCAL_IMAGE="${NEW_API_IMAGE:-calciumion/new-api:latest}"
WEB_HTTP_PORT="${WEB_HTTP_PORT:-3000}"
GREEN_HTTP_PORT="${GREEN_HTTP_PORT:-3001}"
BLUE_NODE_TYPE="${BLUE_NODE_TYPE:-master}"
GREEN_NODE_TYPE="${GREEN_NODE_TYPE:-slave}"
DRAIN_TIMEOUT_SECONDS="${NEW_API_DRAIN_TIMEOUT_SECONDS:-1800}"
NGINX_OLD_WORKERS=""

[[ $EUID -eq 0 ]] || { printf 'must run as root\n' >&2; exit 1; }
[[ -f "$COMPOSE_FILE" && -f "$ENV_FILE" ]] || { printf 'compose or env file missing\n' >&2; exit 1; }

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

HOST_IP="${HOST_IP:-127.0.0.1}"
WEB_HTTP_PORT="${WEB_HTTP_PORT:-3000}"
GREEN_HTTP_PORT="${GREEN_HTTP_PORT:-3001}"
LOCAL_IMAGE="${NEW_API_IMAGE:-$LOCAL_IMAGE}"
BLUE_NODE_TYPE="${BLUE_NODE_TYPE:-master}"
GREEN_NODE_TYPE="${GREEN_NODE_TYPE:-slave}"

log() {
  local line="[$(date '+%F %T%z')] $*"
  printf '%s\n' "$line" | tee -a "$RUN_LOG"
}

die() {
  log "ERROR: $*"
  exit 1
}

compose() {
  docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"
}

compose_env() {
  local image="$1" blue_type="$2" green_type="$3"
  shift 3
  env NEW_API_IMAGE="$image" BLUE_NODE_TYPE="$blue_type" GREEN_NODE_TYPE="$green_type" \
    docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"
}

service_for_slot() {
  [[ "$1" == blue ]] && printf 'new-api\n' || printf 'new-api-green\n'
}

port_for_slot() {
  [[ "$1" == blue ]] && printf '%s\n' "$WEB_HTTP_PORT" || printf '%s\n' "$GREEN_HTTP_PORT"
}

container_for_slot() {
  local id
  id="$(compose ps -q "$(service_for_slot "$1")" 2>/dev/null | head -n 1 || true)"
  [[ -n "$id" ]] && printf '%s\n' "$id"
}

image_id_for() {
  docker image inspect -f '{{.Id}}' "$1" 2>/dev/null || true
}

resolve_image_ref() {
  local ref="$1" digest
  [[ -n "$(image_id_for "$ref")" ]] && { printf '%s\n' "$ref"; return 0; }
  if [[ "$ref" == *@sha256:* ]]; then
    digest="sha256:${ref##*@sha256:}"
    [[ -n "$(image_id_for "$digest")" ]] && { printf '%s\n' "$digest"; return 0; }
  fi
  return 1
}

image_id_for_container() {
  docker inspect -f '{{.Image}}' "$1"
}

version_for_image() {
  local version
  version="$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' "$1" 2>/dev/null || true)"
  [[ "$version" == '<no value>' ]] && version=""
  printf '%s\n' "$version"
}

version_for_slot() {
  local port="$1" version
  version="$(curl -fsS -D - -o /dev/null --max-time 5 "http://127.0.0.1:${port}/" 2>/dev/null \
    | awk -F': ' 'tolower($1)=="x-new-api-version" {gsub("\r", "", $2); print $2; exit}' || true)"
  [[ -n "$version" ]] && printf '%s\n' "$version" || printf 'unknown\n'
}

write_state() {
  local active_slot="$1" active_image="$2" active_digest="$3" active_version="$4"
  local previous_slot="$5" previous_image="$6" previous_digest="$7" previous_version="$8" tmp
  install -d -m 700 "$BASE_DIR"
  tmp="$(mktemp "$BASE_DIR/state.env.XXXXXX")"
  {
    printf 'ACTIVE_SLOT=%q\n' "$active_slot"
    printf 'ACTIVE_IMAGE=%q\n' "$active_image"
    printf 'ACTIVE_DIGEST=%q\n' "$active_digest"
    printf 'ACTIVE_VERSION=%q\n' "$active_version"
    printf 'PREVIOUS_SLOT=%q\n' "$previous_slot"
    printf 'PREVIOUS_IMAGE=%q\n' "$previous_image"
    printf 'PREVIOUS_DIGEST=%q\n' "$previous_digest"
    printf 'PREVIOUS_VERSION=%q\n' "$previous_version"
    printf 'LAST_SWITCH_AT=%q\n' "$(date --iso-8601=seconds)"
  } > "$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

load_state() {
  [[ -f "$STATE_FILE" ]] || die "state is not initialized; run init first"
  # shellcheck disable=SC1090
  source "$STATE_FILE"
  [[ "${ACTIVE_SLOT:-}" == blue || "${ACTIVE_SLOT:-}" == green ]] || die "invalid ACTIVE_SLOT"
  [[ -n "${ACTIVE_IMAGE:-}" ]] || die "ACTIVE_IMAGE is missing"
}

wait_slot() {
  local slot="$1" timeout="${2:-180}" service id port end status body
  service="$(service_for_slot "$slot")"
  port="$(port_for_slot "$slot")"
  end=$(( $(date +%s) + timeout ))
  while (( $(date +%s) < end )); do
    id="$(container_for_slot "$slot" || true)"
    if [[ -n "$id" ]]; then
      status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null || true)"
      case "$status" in
        restarting|exited|dead) break ;;
      esac
      if [[ "$status" == healthy ]]; then
        body="$(curl -fsS --max-time 5 "http://127.0.0.1:${port}/api/status" 2>/dev/null || true)"
        if grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<<"$body"; then
          log "$slot is healthy: service=$service container=$id port=$port"
          return 0
        fi
      fi
    fi
    sleep 2
  done
  [[ -n "${id:-}" ]] && docker logs --tail 120 "$id" >> "$RUN_LOG" 2>&1 || true
  return 1
}

recreate_slot() {
  local slot="$1" role="$2" image="$3" service blue_type green_type
  service="$(service_for_slot "$slot")"
  blue_type="$BLUE_NODE_TYPE"
  green_type="$GREEN_NODE_TYPE"
  if [[ "$slot" == blue ]]; then blue_type="$role"; else green_type="$role"; fi
  log "recreating $slot as $role with $image"
  compose_env "$image" "$blue_type" "$green_type" up -d --pull never --no-deps --force-recreate "$service" >> "$RUN_LOG" 2>&1
}

ensure_nginx_hook() {
  install -d -m 755 "$(dirname "$NGINX_SWITCH_FILE")"
  if grep -Fq 'proxy_pass http://$newapi_backend;' "$NGINX_VHOST"; then
    return 0
  fi
  local tmp
  tmp="$(mktemp "${NGINX_VHOST}.bluegreen.XXXXXX")"
  awk '
    BEGIN { replaced = 0 }
    !replaced && $0 ~ /^[[:space:]]*proxy_pass[[:space:]]+http:\/\/127\.0\.0\.1:[0-9][0-9]*;[[:space:]]*$/ {
      match($0, /^[[:space:]]*/)
      indent = substr($0, RSTART, RLENGTH)
      print indent "proxy_pass http://$newapi_backend;"
      replaced = 1
      next
    }
    { print }
    END { if (!replaced) exit 2 }
  ' "$NGINX_VHOST" > "$tmp" || {
    rm -f "$tmp"
    die "could not install blue-green include in $NGINX_VHOST"
  }
  cp -a "$NGINX_VHOST" "$NGINX_VHOST.before-bluegreen"
  mv -f "$tmp" "$NGINX_VHOST"
  if ! "$NGINX_BIN" -t -c "$NGINX_CONF" >> "$RUN_LOG" 2>&1; then
    cp -a "$NGINX_VHOST.before-bluegreen" "$NGINX_VHOST"
    die "Nginx configuration test failed after installing blue-green include"
  fi
  if ! "$NGINX_BIN" -s reload -c "$NGINX_CONF" >> "$RUN_LOG" 2>&1; then
    cp -a "$NGINX_VHOST.before-bluegreen" "$NGINX_VHOST"
    return 1
  fi
  log "installed blue-green include in $NGINX_VHOST"
}

capture_nginx_workers() {
  local master=""
  NGINX_OLD_WORKERS=""
  [[ -s /www/server/nginx/logs/nginx.pid ]] && master="$(head -n 1 /www/server/nginx/logs/nginx.pid | tr -d '[:space:]')"
  [[ -n "$master" ]] && NGINX_OLD_WORKERS="$(ps -eo pid=,ppid=,cmd= | awk -v master="$master" '$2 == master && $0 ~ /nginx: worker/ {print $1}')"
}

write_backend() {
  local slot="$1" port tmp old_content
  port="$(port_for_slot "$slot")"
  old_content=""
  [[ -f "$NGINX_SWITCH_FILE" ]] && old_content="$(<"$NGINX_SWITCH_FILE")"
  tmp="$(mktemp "${NGINX_SWITCH_FILE}.XXXXXX")"
  printf '# Managed by newapi-bluegreen.sh; do not edit manually.\nset $newapi_backend 127.0.0.1:%s;\n' "$port" > "$tmp"
  chmod 644 "$tmp"
  mv -f "$tmp" "$NGINX_SWITCH_FILE"
  if ! "$NGINX_BIN" -t -c "$NGINX_CONF" >> "$RUN_LOG" 2>&1; then
    [[ -n "$old_content" ]] && printf '%s\n' "$old_content" > "$NGINX_SWITCH_FILE" || rm -f "$NGINX_SWITCH_FILE"
    return 1
  fi
  capture_nginx_workers
  if ! "$NGINX_BIN" -s reload -c "$NGINX_CONF" >> "$RUN_LOG" 2>&1; then
    [[ -n "$old_content" ]] && printf '%s\n' "$old_content" > "$NGINX_SWITCH_FILE" || rm -f "$NGINX_SWITCH_FILE"
    return 1
  fi
  # HUP already asks old workers to finish gracefully; QUIT also closes idle
  # keep-alive clients so only active upstream streams remain to be drained.
  for pid in $NGINX_OLD_WORKERS; do kill -QUIT "$pid" 2>/dev/null || true; done
  log "Nginx backend switched to $slot (127.0.0.1:$port)"
}

probe_public() {
  local body
  body="$(curl -kfsS --max-time 10 --resolve ai.createsis.cn:443:127.0.0.1 https://ai.createsis.cn/api/status 2>/dev/null || true)"
  grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<<"$body"
}

old_backend_connections() {
  local port="$1"
  command -v ss >/dev/null 2>&1 || return 2
  ss -Hnt state established "( sport = :$port or dport = :$port )" 2>/dev/null | awk 'NF {n++} END {print n + 0}'
}

drain_old_slot() {
  local slot="$1" port="$2" elapsed connections
  for elapsed in $(seq 0 2 "$DRAIN_TIMEOUT_SECONDS"); do
    if ! connections="$(old_backend_connections "$port")"; then
      log "WARNING: cannot inspect old $slot connections; leaving it running"
      return 1
    fi
    if [[ "$connections" =~ ^[0-9]+$ ]] && (( connections == 0 )); then
      NGINX_OLD_WORKERS=""
      log "old Nginx workers for $slot closed after upstream drain"
      return 0
    fi
    (( elapsed < DRAIN_TIMEOUT_SECONDS )) && sleep 2
  done
  log "WARNING: keeping old $slot active for ${connections:-unknown} upstream connection(s)"
  return 1
}

retire_slot() {
  local slot="$1" service id port connections
  service="$(service_for_slot "$slot")"
  id="$(container_for_slot "$slot" || true)"
  [[ -n "$id" ]] || return 0
  port="$(port_for_slot "$slot")"
  if ! connections="$(old_backend_connections "$port")"; then
    log "WARNING: cannot inspect $slot connections; leaving container $id running"
    return 1
  fi
  if [[ ! "$connections" =~ ^[0-9]+$ ]] || (( connections != 0 )); then
    log "WARNING: keeping $slot container $id; ${connections:-unknown} upstream connection(s) remain"
    return 1
  fi
  if ! compose rm -sf "$service" >> "$RUN_LOG" 2>&1; then
    log "WARNING: failed to remove retired $slot container $id"
    return 1
  fi
  log "retired $slot container $id; only the active slot remains connected"
}

init_state() {
  local id image digest version
  id="$(container_for_slot blue || true)"
  [[ -n "$id" ]] || die "blue container is not running"
  wait_slot blue 120 || die "blue is not healthy"
  image="$(image_id_for_container "$id")"
  digest="$(docker inspect -f '{{.Image}}' "$id")"
  version="$(version_for_slot "$WEB_HTTP_PORT")"
  ensure_nginx_hook
  write_backend blue
  write_state blue "$image" "$digest" "$version" "" "" "" ""
  log "state initialized: active=blue image=$image version=$version"
}

status_cmd() {
  [[ -f "$STATE_FILE" ]] && source "$STATE_FILE"
  printf 'active_slot=%s\n' "${ACTIVE_SLOT:-unknown}"
  printf 'active_image=%s\n' "${ACTIVE_IMAGE:-unknown}"
  compose ps
  printf 'nginx_backend='; grep -E '^[[:space:]]*set[[:space:]]+\$newapi_backend' "$NGINX_SWITCH_FILE" 2>/dev/null || printf 'missing\n'
}

verify_cmd() {
  load_state
  ensure_nginx_hook
  wait_slot "$ACTIVE_SLOT" 30 || die "active slot is unhealthy"
  probe_public || die "public domain probe failed"
  grep -q "127.0.0.1:$(port_for_slot "$ACTIVE_SLOT")" "$NGINX_SWITCH_FILE" || die "Nginx backend does not match active slot"
  log "verification passed for active=$ACTIVE_SLOT version=$(version_for_slot "$(port_for_slot "$ACTIVE_SLOT")")"
}

repair_nginx_cmd() {
  load_state
  ensure_nginx_hook
  write_backend "$ACTIVE_SLOT" || die "failed to write active Nginx backend"
  probe_public || die "public domain probe failed after Nginx repair"
  log "Nginx repair completed for active=$ACTIVE_SLOT"
}

deploy_cmd() {
  local old_slot new_slot old_image old_digest old_version old_id old_container_image inactive_id inactive_image
  local local_id new_image new_digest new_version
  load_state
  old_slot="$ACTIVE_SLOT"
  new_slot="$([[ "$old_slot" == blue ]] && printf green || printf blue)"
  old_image="$ACTIVE_IMAGE"
  old_digest="${ACTIVE_DIGEST:-$ACTIVE_IMAGE}"
  old_version="${ACTIVE_VERSION:-unknown}"
  local_id="$(image_id_for "$LOCAL_IMAGE")"
  [[ -n "$local_id" ]] || die "local image is unavailable: $LOCAL_IMAGE; load it with this tag first"
  old_id="$(container_for_slot "$old_slot" || true)"
  old_container_image=""
  if [[ -n "$old_id" ]]; then
    old_container_image="$(image_id_for_container "$old_id")"
    old_image="$old_container_image"
    old_digest="$old_container_image"
  fi
  if [[ -n "$old_id" ]] && [[ "$old_container_image" == "$local_id" ]]; then
    inactive_id="$(container_for_slot "$new_slot" || true)"
    if [[ -n "$inactive_id" ]]; then
      inactive_image="$(image_id_for_container "$inactive_id")"
      if [[ "${PREVIOUS_SLOT:-}" == "$new_slot" && -n "$inactive_image" ]]; then
        write_state "$old_slot" "$ACTIVE_IMAGE" "${ACTIVE_DIGEST:-$local_id}" "${ACTIVE_VERSION:-unknown}" \
          "$new_slot" "$inactive_image" "$inactive_image" "${PREVIOUS_VERSION:-unknown}"
      fi
      ensure_nginx_hook
      grep -q "127.0.0.1:$(port_for_slot "$old_slot")" "$NGINX_SWITCH_FILE" \
        || die "Nginx backend does not match active slot; run repair-nginx before retiring $new_slot"
      retire_slot "$new_slot" || return 2
    fi
    log "local image $LOCAL_IMAGE is unchanged ($local_id); no update needed"
    return 0
  fi
  new_image="$LOCAL_IMAGE"
  new_digest="$local_id"
  new_version="$(version_for_image "$new_image")"
  log "deploying $new_image to inactive slot=$new_slot while active=$old_slot"

  recreate_slot "$new_slot" slave "$new_image" || { log "candidate recreation failed"; return 1; }
  wait_slot "$new_slot" 180 || { log "candidate health failed; active=$old_slot unchanged"; compose rm -sf "$(service_for_slot "$new_slot")" >> "$RUN_LOG" 2>&1 || true; return 1; }
  recreate_slot "$new_slot" master "$new_image" || {
    log "candidate master promotion failed; removing candidate"
    compose rm -sf "$(service_for_slot "$new_slot")" >> "$RUN_LOG" 2>&1 || true
    return 1
  }
  wait_slot "$new_slot" 180 || {
    log "candidate failed after master promotion; removing candidate"
    compose rm -sf "$(service_for_slot "$new_slot")" >> "$RUN_LOG" 2>&1 || true
    return 1
  }

  ensure_nginx_hook
  if ! write_backend "$new_slot" || ! probe_public; then
    log "traffic probe failed after switch; restoring $old_slot"
    write_backend "$old_slot" >> "$RUN_LOG" 2>&1 || true
    compose rm -sf "$(service_for_slot "$new_slot")" >> "$RUN_LOG" 2>&1 || true
    return 1
  fi
  write_state "$new_slot" "$new_image" "$new_digest" "${new_version:-unknown}" "$old_slot" "$old_image" "$old_digest" "$old_version"

  if drain_old_slot "$old_slot" "$(port_for_slot "$old_slot")"; then
    if retire_slot "$old_slot"; then
      log "deploy completed: active=$new_slot previous=$old_slot version=${new_version:-unknown}"
      return 0
    fi
    log "deploy switched successfully but old slot demotion failed"
    return 2
  fi
  log "deploy switched successfully; old slot remains serving existing upstreams"
  return 2
}

rollback_cmd() {
  local old_slot target_slot old_image target_image old_digest target_digest old_version target_version target_id target_container_image
  load_state
  [[ -n "${PREVIOUS_SLOT:-}" && -n "${PREVIOUS_IMAGE:-}" ]] || die "no previous deployment is recorded"
  old_slot="$ACTIVE_SLOT"
  target_slot="$PREVIOUS_SLOT"
  old_image="$ACTIVE_IMAGE"
  target_image="$PREVIOUS_IMAGE"
  old_digest="${ACTIVE_DIGEST:-$ACTIVE_IMAGE}"
  target_digest="${PREVIOUS_DIGEST:-$target_image}"
  old_version="${ACTIVE_VERSION:-unknown}"
  target_version="${PREVIOUS_VERSION:-unknown}"
  target_id="$(container_for_slot "$target_slot" || true)"
  target_container_image=""
  if [[ -n "$target_id" ]]; then
    target_container_image="$(image_id_for_container "$target_id")"
    target_image="$target_container_image"
    target_digest="$target_container_image"
  fi
  target_image="$(resolve_image_ref "$target_image" || true)"
  [[ -n "$target_image" ]] || die "rollback image is unavailable locally: ${PREVIOUS_IMAGE:-unknown}"
  target_digest="$target_image"
  recreate_slot "$target_slot" master "$target_image" || return 1
  wait_slot "$target_slot" 180 || {
    compose rm -sf "$(service_for_slot "$target_slot")" >> "$RUN_LOG" 2>&1 || true
    return 1
  }
  ensure_nginx_hook
  write_backend "$target_slot" && probe_public || {
    log "rollback probe failed; restoring $old_slot"
    write_backend "$old_slot" >> "$RUN_LOG" 2>&1 || true
    compose rm -sf "$(service_for_slot "$target_slot")" >> "$RUN_LOG" 2>&1 || true
    return 1
  }
  write_state "$target_slot" "$target_image" "$target_digest" "$target_version" "$old_slot" "$old_image" "$old_digest" "$old_version"
  if drain_old_slot "$old_slot" "$(port_for_slot "$old_slot")" && retire_slot "$old_slot"; then
    log "rollback completed: active=$target_slot previous=$old_slot"
    return 0
  fi
  log "rollback switched successfully but old slot demotion needs attention"
  return 2
}

usage() {
  printf 'usage: %s {init|status|verify|repair-nginx|deploy [local-image-ref]|rollback}\n' "$0"
}

install -d -m 700 "$BASE_DIR"
touch "$RUN_LOG"
chmod 600 "$RUN_LOG"
exec 9>"$LOCK_FILE"
flock -n 9 || die "another blue-green operation is running"

case "${1:-}" in
  init) init_state ;;
  status) status_cmd ;;
  verify) verify_cmd ;;
  repair-nginx) repair_nginx_cmd ;;
  deploy) deploy_cmd ;;
  rollback) rollback_cmd ;;
  *) usage; exit 2 ;;
esac
