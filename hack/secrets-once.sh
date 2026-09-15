#!/usr/bin/env bash
# Реквизиты кластерного прогона — то, что прогон не может породить сам, потому
# что они нужны ему для старта: доступ участника-песочницы, личность CI в
# облаке, корневой сертификат и токены кластера (они привязаны к подключению
# на платформе, а отзывать подключения она не умеет — перевыпускать нельзя),
# токен реестра и установка GitHub App (у GitHub нет API для них).
#
# Всё остальное — продукт, подключение хостинга кода, креденшел реестра, адрес
# и DNS, подключение кластера, окружение и зона — обеспечивает себе сам прогон.
#
# Идемпотентен: выпущенное однажды хранится в STATE_DIR и переиспользуется,
# секреты перезаписываются теми же значениями. Истёкший токен песочницы
# перевыпускается. Нужны: yc с профилем владельца, gh с правом писать секреты,
# openssl, python3, curl.
set -euo pipefail

STATE_DIR="${E2E_STATE_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/paas-e2e}"
REPO="${E2E_REPO:-TraumTech/paas-e2e}"
PAAS_API_URL="${PAAS_API_URL:-https://api.paas.traumtech.ru}"
PAAS_AUTH_URL="${PAAS_AUTH_URL:-https://auth.paas.traumtech.ru}"
SANDBOX_EMAIL="${E2E_SANDBOX_EMAIL:-paas-e2e@traumtech.ru}"
SA_NAME="${E2E_SA_NAME:-paas-e2e-ci}"

log()  { printf '\n▸ %s\n' "$*" >&2; }
note() { printf '  %s\n' "$*" >&2; }
die()  { printf 'ошибка: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
jbody() { python3 -c 'import json,sys; print(json.dumps(dict(a.split("=",1) for a in sys.argv[1:]), ensure_ascii=False))' "$@"; }
secret() { gh secret set "$1" -R "$REPO" --body "$2" >/dev/null; note "секрет $1 записан"; }
state_path() { printf '%s/%s' "$STATE_DIR" "$1"; }
state_has()  { [ -s "$(state_path "$1")" ]; }
state_get()  { cat "$(state_path "$1")"; }
state_put()  { printf '%s' "$2" > "$(state_path "$1")"; chmod 600 "$(state_path "$1")"; }

# api МЕТОД ПУТЬ [ТЕЛО] — запрос к платформе от лица песочницы.
api() {
  local method="$1" path="$2" body="${3:-}" out code
  local -a auth
  if [ -n "${SANDBOX_TOKEN:-}" ]; then auth=(-H "Authorization: Bearer ${SANDBOX_TOKEN}"); else auth=(-H "X-Session-Token: ${SANDBOX_SESSION}"); fi
  out="$(mktemp)"
  code="$(curl -s --max-time 60 -o "$out" -w '%{http_code}' -X "$method" "${PAAS_API_URL}${path}" \
    -H 'Accept: application/json' -H 'Content-Type: application/json' "${auth[@]}" ${body:+--data "$body"})"
  if [ "${code:0:1}" != "2" ]; then
    printf 'платформа отказала на %s %s: %s %s\n' "$method" "$path" "$code" "$(cat "$out")" >&2
    rm -f "$out"; return 1
  fi
  cat "$out"; rm -f "$out"
}

# kratos ПОТОК ТЕЛО — нативный (API) self-service flow; печатает код ответа,
# тело — в $KRATOS_REPLY.
KRATOS_REPLY="$(mktemp)"
trap 'rm -f "$KRATOS_REPLY"' EXIT
kratos() {
  local flow
  flow="$(curl -sf "${PAAS_AUTH_URL}/self-service/$1/api" -H 'Accept: application/json' | jget 'd["id"]')"
  curl -s -o "$KRATOS_REPLY" -w '%{http_code}' -X POST "${PAAS_AUTH_URL}/self-service/$1?flow=${flow}" \
    -H 'Content-Type: application/json' -H 'Accept: application/json' --data "$2"
}

mkdir -p "$STATE_DIR"; chmod 700 "$STATE_DIR"

# ---------------------------------------------------------------------------
log "1/5 доступ участника-песочницы ${SANDBOX_EMAIL}"

SANDBOX_TOKEN=""; SANDBOX_SESSION=""
if state_has sandbox.token; then
  SANDBOX_TOKEN="$(state_get sandbox.token)"
  api GET /organizations/current >/dev/null 2>&1 || { note "сохранённый токен не принят — выпускаем заново"; SANDBOX_TOKEN=""; }
fi
if [ -z "$SANDBOX_TOKEN" ]; then
  state_has sandbox.password || state_put sandbox.password "$(openssl rand -base64 24)"
  password="$(state_get sandbox.password)"
  # Регистрация: хук session отдаёт сессию сразу, подтверждения почты нет.
  # Участник уже есть — обычный вход тем же паролем.
  registration="$(python3 -c 'import json,sys; print(json.dumps({"method":"password","password":sys.argv[2],"traits":{"email":sys.argv[1]}}))' "$SANDBOX_EMAIL" "$password")"
  if [ "$(kratos registration "$registration")" = "200" ]; then
    note "участник зарегистрирован"
  else
    [ "$(kratos login "$(jbody method=password "identifier=${SANDBOX_EMAIL}" "password=${password}")")" = "200" ] \
      || die "вход участником ${SANDBOX_EMAIL} не прошёл: $(cat "$KRATOS_REPLY"). Пароль берётся из $(state_path sandbox.password)."
  fi
  SANDBOX_SESSION="$(jget 'd["session_token"]' < "$KRATOS_REPLY")"
  # Личный токен живёт не дольше года; повторный запуск выпустит новый.
  SANDBOX_TOKEN="$(api POST /tokens '{"name":"e2e-cluster-run","lifetime_days":365}' | jget 'd["secret"]')"
  curl -s -o /dev/null -X DELETE "${PAAS_AUTH_URL}/self-service/logout/api" -H 'Content-Type: application/json' \
    --data "$(jbody "session_token=${SANDBOX_SESSION}")" || true
  SANDBOX_SESSION=""
  state_put sandbox.token "$SANDBOX_TOKEN"
  note "личный токен выпущен"
fi
secret PAAS_SANDBOX_API_TOKEN "$SANDBOX_TOKEN"

# ---------------------------------------------------------------------------
log "2/5 установка GitHub App в аккаунт песочницы"

if [ -n "${E2E_GITHUB_INSTALLATION_ID:-}" ]; then
  state_put github.installation "$E2E_GITHUB_INSTALLATION_ID"
fi
if ! state_has github.installation; then
  state="$(api POST /organizations/current/scm-connections/install-intent | python3 -c 'import json,sys,urllib.parse as u; print(u.parse_qs(u.urlparse(json.load(sys.stdin)["install_url"]).query)["state"][0])')"
  cat >&2 <<MSG

  Приложение платформы должно быть публичным (настройки GitHub App → Advanced
  → Make public), иначе GitHub молча уведёт на существующую установку.
  Откройте, войдя в GitHub владельцем аккаунта или организации песочницы:

    https://github.com/apps/traumtech-paas/installations/new?state=${state}

  Выберите аккаунт песочницы и установите. GitHub вернёт на платформу адресом
  с installation_id — вставьте адрес или само число (оно же в адресе страницы
  приложения: настройки аккаунта → Installed GitHub Apps).

MSG
  until state_has github.installation; do
    printf '  адрес возврата или installation_id: ' >&2; read -r answer
    id="$(python3 -c 'import sys,urllib.parse as u; a=sys.argv[1].strip(); print(a if a.isdigit() else u.parse_qs(u.urlparse(a).query)["installation_id"][0])' "$answer" 2>/dev/null || true)"
    [ -n "$id" ] && state_put github.installation "$id" || note "не разобрал"
  done
fi
secret E2E_GITHUB_INSTALLATION_ID "$(state_get github.installation)"

# ---------------------------------------------------------------------------
log "3/5 токен реестра ghcr.io"

if [ -n "${E2E_GHCR_TOKEN:-}" ]; then
  state_put ghcr.user "${E2E_GHCR_USER:?логин владельца токена — E2E_GHCR_USER}"
  state_put ghcr.token "$E2E_GHCR_TOKEN"
fi
if ! state_has ghcr.token; then
  cat >&2 <<MSG

  Нужен classic PAT владельца аккаунта песочницы с правом read:packages, без
  срока: github.com/settings/tokens/new?scopes=read:packages
  API для выпуска токенов у GitHub нет.

MSG
  printf '  логин GitHub владельца токена: ' >&2; read -r user
  printf '  токен (ввод скрыт): ' >&2; read -rs token; echo >&2
  state_put ghcr.user "$user"; state_put ghcr.token "$token"
fi
secret E2E_GHCR_USER "$(state_get ghcr.user)"
secret E2E_GHCR_TOKEN "$(state_get ghcr.token)"

# ---------------------------------------------------------------------------
log "4/5 сервисный аккаунт ${SA_NAME}: личность CI в облаке"

FOLDER_ID="$(yc config get folder-id)"
sa_id="$(yc iam service-account get --name "$SA_NAME" --format json 2>/dev/null | jget 'd["id"]' || true)"
if [ -z "$sa_id" ]; then
  sa_id="$(yc iam service-account create --name "$SA_NAME" \
    --description "CI paas-e2e: адрес, DNS и машина кластера прогона" --format json | jget 'd["id"]')"
  note "сервисный аккаунт создан"
fi
# compute.editor — машина; vpc.user — подсеть; vpc.publicAdmin — зарезервированный
# адрес; dns.editor — записи под кластер прогона.
for role in compute.editor vpc.user vpc.publicAdmin dns.editor; do
  yc resource-manager folder add-access-binding "$FOLDER_ID" --role "$role" \
    --subject "serviceAccount:${sa_id}" >/dev/null 2>&1 || true
done
if ! state_has yc-sa-key.json; then
  yc iam key create --service-account-id "$sa_id" --output "$(state_path yc-sa-key.json)" >/dev/null
  chmod 600 "$(state_path yc-sa-key.json)"
  note "ключ выпущен"
fi
secret YC_SA_KEY "$(state_get yc-sa-key.json)"
secret YC_CLOUD_ID "$(yc config get cloud-id)"
secret YC_FOLDER_ID "$FOLDER_ID"

# ---------------------------------------------------------------------------
log "5/5 корневой сертификат и токены кластера прогона"

if ! state_has ca.crt; then
  # ECDSA P-256 — то же, чем подписывает себя k3s по умолчанию.
  openssl ecparam -genkey -name prime256v1 -out "$(state_path ca.key)" 2>/dev/null
  openssl req -x509 -new -sha256 -days 3650 -key "$(state_path ca.key)" -out "$(state_path ca.crt)" \
    -subj "/CN=paas-e2e-server-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" 2>/dev/null
  chmod 600 "$(state_path ca.key)"
  note "сертификат выпущен"
fi
state_has platform.token || state_put platform.token "$(openssl rand -hex 32)"
state_has admin.token    || state_put admin.token "$(openssl rand -hex 32)"
secret E2E_CA_CRT "$(base64 < "$(state_path ca.crt)" | tr -d '\n')"
secret E2E_CA_KEY "$(base64 < "$(state_path ca.key)" | tr -d '\n')"
secret E2E_PLATFORM_TOKEN "$(state_get platform.token)"
secret E2E_ADMIN_TOKEN "$(state_get admin.token)"

log "реквизиты на месте — прогон: gh workflow run e2e-cluster -R ${REPO}"
