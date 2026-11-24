#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

resolve_audit_bin() {
  if [[ -n "${CONFORMANCE_AUDIT_BIN:-}" && -x "${CONFORMANCE_AUDIT_BIN}" ]]; then
    echo "${CONFORMANCE_AUDIT_BIN}"
    return 0
  fi

  local candidates=(
    "${ROOT_DIR}/.conformance-runner/bin/conformance-audit"
    "${ROOT_DIR}/../backend_security_and_conformance_tests/bin/conformance-audit"
    "${ROOT_DIR}/../backend_boilerplate_conformance_tests/bin/conformance-audit"
  )
  for candidate in "${candidates[@]}"; do
    if [[ -x "${candidate}" ]]; then
      echo "${candidate}"
      return 0
    fi
  done

  if command -v conformance-audit >/dev/null 2>&1; then
    command -v conformance-audit
    return 0
  fi

  local src_dirs=(
    "${ROOT_DIR}/.conformance-runner"
    "${ROOT_DIR}/../backend_security_and_conformance_tests"
    "${ROOT_DIR}/../backend_boilerplate_conformance_tests"
  )
  if command -v go >/dev/null 2>&1; then
    for src_dir in "${src_dirs[@]}"; do
      if [[ -f "${src_dir}/cmd/conformance-audit/main.go" ]]; then
        mkdir -p "${src_dir}/bin"
        (cd "${src_dir}" && go build -o bin/conformance-audit ./cmd/conformance-audit) >&2
        echo "${src_dir}/bin/conformance-audit"
        return 0
      fi
    done
  fi

  echo "Error: conformance-audit binary not found. Set CONFORMANCE_AUDIT_BIN or build ../backend_boilerplate_conformance_tests." >&2
  return 1
}

AUDIT_BIN="$(resolve_audit_bin)"

STAGES="sast,graph,pentest,load"
HAS_URL=false

args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  arg="${args[$i]}"
  case "${arg}" in
    --stages=*)
      STAGES="${arg#--stages=}"
      ;;
    --stages)
      if ((i + 1 < ${#args[@]})); then
        STAGES="${args[$((i + 1))]}"
      fi
      ;;
    --url=*|--url)
      HAS_URL=true
      ;;
  esac
done

is_static_only() {
  local remaining="$1"
  local stage=""
  while [[ -n "${remaining}" ]]; do
    if [[ "${remaining}" == *,* ]]; then
      stage="${remaining%%,*}"
      remaining="${remaining#*,}"
    else
      stage="${remaining}"
      remaining=""
    fi
    case "${stage}" in
      sast|graph) ;;
      *) return 1 ;;
    esac
  done
  return 0
}

if is_static_only "${STAGES}" && [[ "${HAS_URL}" == "false" ]]; then
  exec "${AUDIT_BIN}" \
    --target "${ROOT_DIR}" \
    --manifest "${ROOT_DIR}/boilerplate.manifest.json" \
    "$@"
fi

if [[ "${SKIP_APP_BOOT:-false}" == "true" ]]; then
  exec "${AUDIT_BIN}" \
    --target "${ROOT_DIR}" \
    --manifest "${ROOT_DIR}/boilerplate.manifest.json" \
    "$@"
fi

USER_PORT="${PORT:-}"
USER_APP_ENV="${APP_ENV:-}"
USER_POSTGRES_HOST="${POSTGRES_HOST:-}"
USER_POSTGRES_PORT="${POSTGRES_PORT:-}"
USER_POSTGRES_USER="${POSTGRES_USER:-}"
USER_POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
USER_POSTGRES_DB="${POSTGRES_DB:-}"
USER_POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-}"
USER_REDIS_HOST="${REDIS_HOST:-}"
USER_REDIS_PORT="${REDIS_PORT:-}"
USER_REDIS_PASSWORD="${REDIS_PASSWORD:-}"
USER_MONGO_URI="${MONGO_URI:-}"
USER_RATE_LIMIT_DEFAULT_LIMIT="${RATE_LIMIT_DEFAULT_LIMIT:-}"
USER_OVERLOAD_MAX_IN_FLIGHT="${OVERLOAD_MAX_IN_FLIGHT:-}"
USER_ARGON2_MAX_CONCURRENT="${ARGON2_MAX_CONCURRENT:-}"
USER_ARGON2_QUEUE_DEPTH="${ARGON2_QUEUE_DEPTH:-}"
USER_ARGON2_QUEUE_TIMEOUT_MS="${ARGON2_QUEUE_TIMEOUT_MS:-}"

if [[ -f "${ROOT_DIR}/.env.conformance.example" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env.conformance.example"
  set +a
fi

export APP_ENV="${USER_APP_ENV:-${APP_ENV:-test}}"
export PORT="${USER_PORT:-${PORT:-8080}}"
export POSTGRES_HOST="${USER_POSTGRES_HOST:-127.0.0.1}"
export POSTGRES_PORT="${USER_POSTGRES_PORT:-5432}"
export POSTGRES_USER="${USER_POSTGRES_USER:-postgres}"
export POSTGRES_PASSWORD="${USER_POSTGRES_PASSWORD:-postgres}"
export POSTGRES_DB="${USER_POSTGRES_DB:-app_db}"
export POSTGRES_SSLMODE="${USER_POSTGRES_SSLMODE:-disable}"
export REDIS_HOST="${USER_REDIS_HOST:-127.0.0.1}"
export REDIS_PORT="${USER_REDIS_PORT:-6379}"
export REDIS_PASSWORD="${USER_REDIS_PASSWORD:-}"
export MONGO_URI="${USER_MONGO_URI:-mongodb://127.0.0.1:27017/go-conformance}"
export COOKIE_SECRET="${COOKIE_SECRET:-conformance-cookie-secret-32-chars-min}"
export RATE_LIMIT_DEFAULT_LIMIT="${USER_RATE_LIMIT_DEFAULT_LIMIT:-${RATE_LIMIT_DEFAULT_LIMIT:-100000}}"
export OVERLOAD_MAX_IN_FLIGHT="${USER_OVERLOAD_MAX_IN_FLIGHT:-${OVERLOAD_MAX_IN_FLIGHT:-1000}}"
export ARGON2_MAX_CONCURRENT="${USER_ARGON2_MAX_CONCURRENT:-${ARGON2_MAX_CONCURRENT:-4}}"
export ARGON2_QUEUE_DEPTH="${USER_ARGON2_QUEUE_DEPTH:-${ARGON2_QUEUE_DEPTH:-32}}"
export ARGON2_QUEUE_TIMEOUT_MS="${USER_ARGON2_QUEUE_TIMEOUT_MS:-${ARGON2_QUEUE_TIMEOUT_MS:-10000}}"

TMP_RUNTIME_DIR=""
APP_PID=""

cleanup() {
  if [[ -n "${APP_PID}" ]] && kill -0 "${APP_PID}" 2>/dev/null; then
    kill "${APP_PID}" 2>/dev/null || true
    wait "${APP_PID}" 2>/dev/null || true
  fi
  if [[ -n "${TMP_RUNTIME_DIR}" && -d "${TMP_RUNTIME_DIR}" ]]; then
    rm -rf "${TMP_RUNTIME_DIR}"
  fi
}
trap cleanup EXIT

ensure_tmp_runtime_dir() {
  if [[ -z "${TMP_RUNTIME_DIR}" ]]; then
    TMP_RUNTIME_DIR="$(mktemp -d)"
  fi
}

if [[ -z "${JWT_PRIVATE_KEY:-}" || -z "${JWT_PUBLIC_KEY:-}" ]]; then
  ensure_tmp_runtime_dir
  openssl genrsa -out "${TMP_RUNTIME_DIR}/private.pem" 2048 >/dev/null 2>&1
  openssl rsa -in "${TMP_RUNTIME_DIR}/private.pem" -pubout -out "${TMP_RUNTIME_DIR}/public.pem" >/dev/null 2>&1
  export JWT_PRIVATE_KEY
  export JWT_PUBLIC_KEY
  JWT_PRIVATE_KEY="$(cat "${TMP_RUNTIME_DIR}/private.pem")"
  JWT_PUBLIC_KEY="$(cat "${TMP_RUNTIME_DIR}/public.pem")"
fi

apply_migrations() {
  if [[ "${SKIP_MIGRATIONS:-false}" == "true" ]]; then
    return 0
  fi
  if command -v psql >/dev/null 2>&1; then
    for migration in "${ROOT_DIR}"/migrations/*_*.up.sql; do
      [[ -f "${migration}" ]] || continue
      PGPASSWORD="${POSTGRES_PASSWORD}" psql \
        -h "${POSTGRES_HOST}" \
        -p "${POSTGRES_PORT}" \
        -U "${POSTGRES_USER}" \
        -d "${POSTGRES_DB}" \
        -v ON_ERROR_STOP=1 \
        -f "${migration}" >/dev/null
    done
    return 0
  fi

  # Fallback when psql is not installed locally (uses pgx/gorm from go.mod)
  ensure_tmp_runtime_dir
  cat > "${TMP_RUNTIME_DIR}/apply_migrations.go" <<'EOF'
package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	host := os.Getenv("POSTGRES_HOST")
	port := os.Getenv("POSTGRES_PORT")
	if port == "" {
		port = "5432"
	}
	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	dbName := os.Getenv("POSTGRES_DB")
	sslMode := os.Getenv("POSTGRES_SSLMODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + dbName,
		RawQuery: "sslmode=" + url.QueryEscape(sslMode) + "&connect_timeout=5",
	}

	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "db connect: %v\n", err)
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer func() { _ = sqlDB.Close() }()
	}

	files, err := filepath.Glob(filepath.Join(os.Args[1], "*_*.up.sql"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "glob migrations: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)
	for _, f := range files {
		sqlBytes, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", f, err)
			os.Exit(1)
		}
		if err := db.Exec(string(sqlBytes)).Error; err != nil {
			fmt.Fprintf(os.Stderr, "exec %s: %v\n", f, err)
			os.Exit(1)
		}
	}
}
EOF
  go run "${TMP_RUNTIME_DIR}/apply_migrations.go" "${ROOT_DIR}/migrations"
}

CONFORMANCE_APP_CMD="${CONFORMANCE_APP_CMD:-}"
if [[ -n "${CONFORMANCE_APP_CMD}" ]]; then
  # Allow tests to inject a stub server command
  # shellcheck disable=SC2086
  ${CONFORMANCE_APP_CMD} > /tmp/go-conformance.log 2>&1 &
  APP_PID=$!
else
  apply_migrations

  API_BIN="${CONFORMANCE_API_BIN:-}"
  if [[ -z "${API_BIN}" || ! -x "${API_BIN}" ]]; then
    ensure_tmp_runtime_dir
    API_BIN="${TMP_RUNTIME_DIR}/api"
    go build -o "${API_BIN}" ./cmd/api
  fi

  "${API_BIN}" > /tmp/go-conformance.log 2>&1 &
  APP_PID=$!
fi

HEALTH_URL="http://127.0.0.1:${PORT}/health/live"
READY=false
for _ in $(seq 1 30); do
  if ! kill -0 "${APP_PID}" 2>/dev/null; then
    echo "Error: Go API server exited prematurely. Logs:" >&2
    cat /tmp/go-conformance.log >&2 || true
    exit 1
  fi
  HTTP_CODE="$(curl -s -o /dev/null -w '%{http_code}' "${HEALTH_URL}" || true)"
  if [[ "${HTTP_CODE}" == "200" ]]; then
    READY=true
    break
  fi
  sleep 1
done

if [[ "${READY}" != "true" ]]; then
  echo "Error: Timed out waiting for ${HEALTH_URL}. Logs:" >&2
  cat /tmp/go-conformance.log >&2 || true
  exit 1
fi

# Warm up Go HTTP routes, middleware, and runtime heap before RSS baseline sampling.
for _ in $(seq 1 8); do
  (
    for _ in $(seq 1 50); do
      curl -s -o /dev/null "${HEALTH_URL}" &
    done
    wait
  )
done
sleep 1

export CONFORMANCE_TARGET_PID="${APP_PID}"

AUDIT_HELP="$("${AUDIT_BIN}" --help 2>&1 || true)"
EXTRA_AUDIT_ARGS=()
if grep -qE '(^|[[:space:]])--?pid([[:space:]=]|$)' <<< "${AUDIT_HELP}"; then
  EXTRA_AUDIT_ARGS+=(--pid "${APP_PID}")
fi

"${AUDIT_BIN}" \
  --target "${ROOT_DIR}" \
  --manifest "${ROOT_DIR}/boilerplate.manifest.json" \
  --url "http://127.0.0.1:${PORT}" \
  ${EXTRA_AUDIT_ARGS[@]+"${EXTRA_AUDIT_ARGS[@]}"} \
  "$@"
