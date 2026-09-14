#!/usr/bin/env bash
# Разовая подготовка кластерного прогона целиком: организация-песочница и её
# токен, доступ CI в облако, постоянная часть эфемерного кластера (адрес, DNS,
# корневой сертификат, токены), первый подъём и подключение кластера к
# платформе, окружение и доменная зона песочницы, секреты репозитория проверок.
#
# Идемпотентен: сделанное узнаётся и пропускается, а выпущенное однажды
# (сертификат, токены, пароль песочницы) хранится в STATE_DIR и переиспользуется
# — повторный запуск не ломает записанное на платформе подключение. Руками
# остаются две вещи, которых у GitHub нет в API: установка GitHub App в аккаунт
# песочницы и выпуск токена для ghcr.io; скрипт останавливается и ждёт их.
#
# Создаёт платные ресурсы (зарезервированный адрес, машина на время подъёма).
# Нужны: yc с профилем владельца, gh с правом писать секреты репозитория,
# openssl, python3, curl.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
STATE_DIR="${E2E_STATE_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/paas-e2e}"
REPO="${E2E_REPO:-TraumTech/paas-e2e}"

PAAS_API_URL="${PAAS_API_URL:-https://api.paas.traumtech.ru}"
PAAS_AUTH_URL="${PAAS_AUTH_URL:-https://auth.paas.traumtech.ru}"
SANDBOX_EMAIL="${E2E_SANDBOX_EMAIL:-paas-e2e@traumtech.ru}"

ZONE="${E2E_ZONE:-ru-central1-a}"
SUBNET="${E2E_SUBNET:-default-ru-central1-a}"
DNS_ZONE="${E2E_DNS_ZONE:-traumtech-ru}"
SUBDOMAIN="${E2E_SUBDOMAIN:-e2e}"
DOMAIN="${E2E_DOMAIN:-traumtech.ru}"
ADDRESS_NAME="${E2E_ADDRESS_NAME:-paas-e2e}"
SA_NAME="${E2E_SA_NAME:-paas-e2e-ci}"
CLUSTER_NAME="${E2E_CLUSTER_NAME:-paas-e2e}"
ENVIRONMENT="${E2E_ENVIRONMENT:-dev}"
ZONE_SLUG="${E2E_ZONE_SLUG:-e2e}"

log()  { printf '\n▸ %s\n' "$*" >&2; }
note() { printf '  %s\n' "$*" >&2; }
die()  { printf 'ошибка: %s\n' "$*" >&2; exit 1; }

# jget ВЫРАЖЕНИЕ — значение из JSON на stdin (d — разобранный документ).
jget() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
# jbody КЛЮЧ=ЗНАЧЕНИЕ... — JSON-объект из пар; значения не экранируются руками.
jbody() { python3 -c 'import json,sys; print(json.dumps(dict(a.split("=",1) for a in sys.argv[1:]), ensure_ascii=False))' "$@"; }

secret() { gh secret set "$1" -R "$REPO" --body "$2" >/dev/null; note "секрет $1 записан"; }

state_path() { printf '%s/%s' "$STATE_DIR" "$1"; }
state_has()  { [ -s "$(state_path "$1")" ]; }
state_get()  { cat "$(state_path "$1")"; }
state_put()  { printf '%s' "$2" > "$(state_path "$1")"; chmod 600 "$(state_path "$1")"; }

# api МЕТОД ПУТЬ [ТЕЛО] — запрос к платформе от лица песочницы: личным токеном,
# а пока его нет — сессией Kratos.
api() {
  local method="$1" path="$2" body="${3:-}"
  local -a auth
  if [ -n "${SANDBOX_TOKEN:-}" ]; then
    auth=(-H "Authorization: Bearer ${SANDBOX_TOKEN}")
  else
    auth=(-H "X-Session-Token: ${SANDBOX_SESSION}")
  fi
  local out code
  out="$(mktemp)"
  code="$(curl -s --max-time 120 -o "$out" -w '%{http_code}' -X "$method" "${PAAS_API_URL}${path}" \
    -H 'Accept: application/json' -H 'Content-Type: application/json' "${auth[@]}" \
    ${body:+--data "$body"})"
  if [ "${code:0:1}" != "2" ]; then
    printf 'платформа отказала на %s %s: %s %s\n' "$method" "$path" "$code" "$(cat "$out")" >&2
    rm -f "$out"; return 1
  fi
  cat "$out"; rm -f "$out"
}

# kratos ПУТЬ ТЕЛО — нативный (API) self-service flow: заводим flow, отправляем
# форму; печатает код ответа, тело — в $KRATOS_REPLY.
KRATOS_REPLY="$(mktemp)"
trap 'rm -f "$KRATOS_REPLY"' EXIT
kratos() {
  local flow
  flow="$(curl -sf "${PAAS_AUTH_URL}/self-service/$1/api" -H 'Accept: application/json' | jget 'd["id"]')"
  curl -s -o "$KRATOS_REPLY" -w '%{http_code}' -X POST "${PAAS_AUTH_URL}/self-service/$1?flow=${flow}" \
    -H 'Content-Type: application/json' -H 'Accept: application/json' --data "$2"
}

mkdir -p "$STATE_DIR"; chmod 700 "$STATE_DIR"
FOLDER_ID="$(yc config get folder-id)"
CLOUD_ID="$(yc config get cloud-id)"

# ---------------------------------------------------------------------------
log "1/6 организация-песочница: участник ${SANDBOX_EMAIL}"

SANDBOX_TOKEN=""; SANDBOX_SESSION=""
if state_has sandbox.token; then
  SANDBOX_TOKEN="$(state_get sandbox.token)"
  if ! api GET /organizations/current >/dev/null 2>&1; then
    note "сохранённый токен песочницы не принят (истёк или отозван) — выпускаем заново"
    SANDBOX_TOKEN=""
  fi
fi

if [ -z "$SANDBOX_TOKEN" ]; then
  state_has sandbox.password || state_put sandbox.password "$(openssl rand -base64 24)"
  password="$(state_get sandbox.password)"

  # Регистрация: хук session отдаёт сессию сразу, подтверждения почты нет.
  # Участник уже есть (повторный запуск) — обычный вход тем же паролем.
  registration="$(python3 -c 'import json,sys; print(json.dumps({"method":"password","password":sys.argv[2],"traits":{"email":sys.argv[1]}}))' "$SANDBOX_EMAIL" "$password")"
  if [ "$(kratos registration "$registration")" = "200" ]; then
    note "участник зарегистрирован"
  else
    note "регистрация не прошла — пробуем войти существующим участником"
    login="$(jbody method=password "identifier=${SANDBOX_EMAIL}" "password=${password}")"
    [ "$(kratos login "$login")" = "200" ] \
      || die "вход участником ${SANDBOX_EMAIL} не прошёл: $(cat "$KRATOS_REPLY"). Пароль берётся из $(state_path sandbox.password) — если участник заведён другим, положите его туда."
  fi
  SANDBOX_SESSION="$(jget 'd["session_token"]' < "$KRATOS_REPLY")"

  # Личный токен живёт не дольше года: по истечении повторный запуск скрипта
  # выпустит новый тем же путём.
  SANDBOX_TOKEN="$(api POST /tokens '{"name":"e2e-cluster-run","lifetime_days":365}' | jget 'd["secret"]')"
  curl -s -o /dev/null -X DELETE "${PAAS_AUTH_URL}/self-service/logout/api" \
    -H 'Content-Type: application/json' --data "$(jbody "session_token=${SANDBOX_SESSION}")" || true
  SANDBOX_SESSION=""
  state_put sandbox.token "$SANDBOX_TOKEN"
  note "личный токен песочницы выпущен"
fi
note "организация-песочница: $(api GET /organizations/current | jget 'd["name"]')"
secret PAAS_SANDBOX_API_TOKEN "$SANDBOX_TOKEN"

# Сервис без продукта не создаётся, а у свежей организации продуктов нет.
if [ "$(api GET /organizations/current/products | jget 'len(d["products"])')" = "0" ]; then
  api POST /organizations/current/products '{"name":"e2e"}' >/dev/null
  note "продукт e2e заведён"
fi

# ---------------------------------------------------------------------------
log "2/6 хостинг кода песочницы"

scm_count() { api GET /organizations/current/scm-connections | jget 'len(d["connections"])'; }
if [ "$(scm_count)" = "0" ]; then
  # Нонс живёт недолго; этот — только чтобы GitHub показал выбор аккаунта.
  # Завершать подключение будем свежим: установка на GitHub уже состоялась.
  intent_state() { api POST /organizations/current/scm-connections/install-intent | python3 -c 'import json,sys,urllib.parse as u; print(u.parse_qs(u.urlparse(json.load(sys.stdin)["install_url"]).query)["state"][0])'; }
  cat >&2 <<MSG

  Нужен GitHub-аккаунт или организация ПЕСОЧНИЦЫ — не та, где стоит
  приложение боевой организации: одна установка принадлежит одной организации
  платформы. Приложение должно быть публичным (настройки GitHub App →
  Advanced → Make public), иначе GitHub молча уведёт на существующую
  установку. Откройте, войдя в GitHub владельцем аккаунта песочницы:

    https://github.com/apps/traumtech-paas/installations/new?state=$(intent_state)

  Выберите аккаунт песочницы и установите. GitHub вернёт вас на платформу
  адресом с installation_id — вставьте этот адрес (или один installation_id;
  он же виден в настройках аккаунта GitHub → Installed GitHub Apps → в адресе
  страницы приложения) сюда.

MSG
  while [ "$(scm_count)" = "0" ]; do
    printf '  адрес возврата или installation_id: ' >&2
    read -r answer
    [ -n "$answer" ] || continue
    if installation_id="$(python3 -c 'import sys,urllib.parse as u; a=sys.argv[1].strip(); print(a if a.isdigit() else u.parse_qs(u.urlparse(a).query)["installation_id"][0])' "$answer" 2>/dev/null)"; then
      api POST /organizations/current/scm-connections "$(jbody "installation_id=${installation_id}" "state=$(intent_state)")" >/dev/null \
        || note "подключение не завершено — проверьте, что приложение установлено в аккаунт песочницы"
    else
      note "не разобрал ни адреса с installation_id, ни числа"
    fi
  done
fi
note "хостинг кода: $(api GET /organizations/current/scm-connections | jget 'd["connections"][0]["account_type"]+" "+d["connections"][0]["account_login"]')"

# ---------------------------------------------------------------------------
log "3/6 креденшел реестра ghcr.io — кластер тянет приватный образ заготовки"

has_ghcr() { api GET /organizations/current/registry-credentials | jget 'any(c.get("server")=="ghcr.io" for c in d["credentials"])'; }
if [ -z "${E2E_GHCR_TOKEN:-}" ] && state_has ghcr.token; then
  E2E_GHCR_TOKEN="$(state_get ghcr.token)"
  E2E_GHCR_USER="${E2E_GHCR_USER:-$(state_get ghcr.user)}"
fi
if [ -n "${E2E_GHCR_TOKEN:-}" ] || [ "$(has_ghcr)" != "True" ]; then
  if [ -z "${E2E_GHCR_TOKEN:-}" ]; then
    cat >&2 <<MSG

  Нужен токен с правом read:packages на пакеты аккаунта песочницы — classic
  PAT владельца этого аккаунта: github.com/settings/tokens/new?scopes=read:packages
  (срок — «No expiration»). API для выпуска токенов у GitHub нет.

MSG
    printf '  логин GitHub владельца токена: ' >&2; read -r E2E_GHCR_USER
    printf '  токен (ввод скрыт): ' >&2; read -rs E2E_GHCR_TOKEN; echo >&2
  fi
  : "${E2E_GHCR_USER:?логин владельца токена — E2E_GHCR_USER}"
  api PUT /organizations/current/registry-credentials/ghcr.io \
    "$(jbody "username=${E2E_GHCR_USER}" "token=${E2E_GHCR_TOKEN}")" >/dev/null
  state_put ghcr.user "$E2E_GHCR_USER"
  state_put ghcr.token "$E2E_GHCR_TOKEN"
  note "креденшел ghcr.io сохранён"
else
  note "креденшел ghcr.io уже есть"
fi

# ---------------------------------------------------------------------------
log "4/6 сервисный аккаунт ${SA_NAME}: CI поднимает и сносит машину"

sa_id="$(yc iam service-account get --name "$SA_NAME" --format json 2>/dev/null | jget 'd["id"]' || true)"
if [ -z "$sa_id" ]; then
  sa_id="$(yc iam service-account create --name "$SA_NAME" \
    --description "CI paas-e2e: поднимает и сносит машину кластера прогона" --format json | jget 'd["id"]')"
  note "сервисный аккаунт создан"
fi
# compute.editor — машина; vpc.user — подсеть; vpc.publicAdmin — зарезервированный адрес.
for role in compute.editor vpc.user vpc.publicAdmin; do
  yc resource-manager folder add-access-binding "$FOLDER_ID" --role "$role" \
    --subject "serviceAccount:${sa_id}" >/dev/null 2>&1 || true
done
if ! state_has yc-sa-key.json; then
  yc iam key create --service-account-id "$sa_id" --output "$(state_path yc-sa-key.json)" >/dev/null
  chmod 600 "$(state_path yc-sa-key.json)"
  note "ключ выпущен"
fi
secret YC_SA_KEY "$(state_get yc-sa-key.json)"
secret YC_CLOUD_ID "$CLOUD_ID"
secret YC_FOLDER_ID "$FOLDER_ID"

# ---------------------------------------------------------------------------
log "5/6 постоянная часть кластера: адрес, DNS, корневой сертификат, токены"

if ! yc vpc address get --name "$ADDRESS_NAME" >/dev/null 2>&1; then
  yc vpc address create --name "$ADDRESS_NAME" --external-ipv4 "zone=${ZONE}" >/dev/null
  note "адрес зарезервирован"
fi
IP="$(yc vpc address get --name "$ADDRESS_NAME" --format json | jget 'd["external_ipv4_address"]["address"]')"
note "адрес: ${IP}"

# Wildcard нужен: хост выкатки складывается из имени сервиса, а он каждый прогон
# новый. Общий *.traumtech.ru смотрит на вход постоянного кластера, поэтому
# записи поддомена заводятся явно.
yc dns zone add-records --name "$DNS_ZONE" \
  --record "${SUBDOMAIN} 300 A ${IP}" \
  --record "*.${SUBDOMAIN} 300 A ${IP}" >/dev/null 2>&1 || note "записи DNS уже есть"

if ! state_has ca.crt; then
  # ECDSA P-256 — то же, чем подписывает себя k3s по умолчанию.
  openssl ecparam -genkey -name prime256v1 -out "$(state_path ca.key)" 2>/dev/null
  openssl req -x509 -new -sha256 -days 3650 -key "$(state_path ca.key)" -out "$(state_path ca.crt)" \
    -subj "/CN=paas-e2e-server-ca" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" 2>/dev/null
  chmod 600 "$(state_path ca.key)"
  note "корневой сертификат выпущен"
fi
state_has platform.token || state_put platform.token "$(openssl rand -hex 32)"
state_has admin.token    || state_put admin.token "$(openssl rand -hex 32)"

export E2E_CLUSTER_HOST="${SUBDOMAIN}.${DOMAIN}"
export E2E_CLUSTER_IP="$IP"
export E2E_CA_CRT; E2E_CA_CRT="$(base64 < "$(state_path ca.crt)" | tr -d '\n')"
export E2E_CA_KEY; E2E_CA_KEY="$(base64 < "$(state_path ca.key)" | tr -d '\n')"
export E2E_PLATFORM_TOKEN; E2E_PLATFORM_TOKEN="$(state_get platform.token)"
export E2E_ADMIN_TOKEN;    E2E_ADMIN_TOKEN="$(state_get admin.token)"
export E2E_SUBNET="$SUBNET"

secret E2E_CLUSTER_HOST "$E2E_CLUSTER_HOST"
secret E2E_CLUSTER_IP "$E2E_CLUSTER_IP"
secret E2E_CA_CRT "$E2E_CA_CRT"
secret E2E_CA_KEY "$E2E_CA_KEY"
secret E2E_PLATFORM_TOKEN "$E2E_PLATFORM_TOKEN"
secret E2E_ADMIN_TOKEN "$E2E_ADMIN_TOKEN"
secret E2E_SUBNET "$E2E_SUBNET"

# ---------------------------------------------------------------------------
log "6/6 первый подъём: подключение кластера к платформе, окружение, доменная зона"

cluster_json() { api GET /organizations/current/clusters | jget "next((json.dumps(c) for c in d['clusters'] if c['name']=='${CLUSTER_NAME}'), '')"; }

if [ -z "$(cluster_json)" ]; then
  # Платформа проверяет связь до сохранения — кластер в этот момент должен
  # стоять; сносится он при любом исходе.
  export PAAS_API_URL PAAS_API_TOKEN="$SANDBOX_TOKEN" KUBECONFIG_OUT="${STATE_DIR}/kubeconfig"
  trap 'rm -f "$KRATOS_REPLY"; "$HERE/cluster-down.sh"' EXIT
  "$HERE/cluster-up.sh"
  connect="$(python3 -c 'import json,sys; print(json.dumps({"name":sys.argv[1],"topology":"zonal","endpoint":"https://"+sys.argv[2]+":6443","ca_certificate":open(sys.argv[3]).read(),"token":sys.argv[4]}))' \
    "$CLUSTER_NAME" "$E2E_CLUSTER_HOST" "$(state_path ca.crt)" "$E2E_PLATFORM_TOKEN")"
  api POST /organizations/current/clusters "$connect" >/dev/null
  note "кластер ${CLUSTER_NAME} подключён — запись живёт дольше самого кластера"
else
  note "кластер ${CLUSTER_NAME} уже подключён"
fi

cluster="$(cluster_json)"
cluster_id="$(printf '%s' "$cluster" | jget 'd["id"]')"
if [ "$(printf '%s' "$cluster" | jget 'd.get("environment") or ""')" != "$ENVIRONMENT" ]; then
  api PUT "/organizations/current/environments/${ENVIRONMENT}/cluster" "$(jbody "cluster_id=${cluster_id}")" >/dev/null
  note "кластер обслуживает окружение ${ENVIRONMENT}"
fi

if [ "$(api GET /organizations/current/domain-zones | jget "any(z['slug']=='${ZONE_SLUG}' for z in d['zones'])")" != "True" ]; then
  api POST /organizations/current/domain-zones "$(jbody "slug=${ZONE_SLUG}" "name=Кластер прогона")" >/dev/null
fi
api PUT "/organizations/current/domain-zones/${ZONE_SLUG}/hosts/${ENVIRONMENT}" "$(jbody "base_host=${E2E_CLUSTER_HOST}")" >/dev/null
note "зона ${ZONE_SLUG}: ${E2E_CLUSTER_HOST} в ${ENVIRONMENT}"

log "готово — запускаем кластерный прогон"
gh workflow run e2e-cluster -R "$REPO"
note "следить: gh run watch -R ${REPO}"
