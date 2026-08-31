#!/usr/bin/env bash
# Разовая подготовка постоянной части эфемерного кластера: адрес, имена, корневой
# сертификат и токены. Всё это переживает пересоздание кластера — именно поэтому
# подключение кластера на платформе заводится один раз, а не каждый прогон.
#
# Запускается руками и один раз. Повторный запуск не нужен: он выпустит новый
# корневой сертификат, и записанное на платформе подключение перестанет
# сходиться.
#
# Создаёт платные ресурсы (зарезервированный адрес) — запускать осознанно.
set -euo pipefail

ZONE="${E2E_ZONE:-ru-central1-a}"
DNS_ZONE="${E2E_DNS_ZONE:-traumtech-ru}"
SUBDOMAIN="${E2E_SUBDOMAIN:-e2e}"
DOMAIN="${E2E_DOMAIN:-traumtech.ru}"
ADDRESS_NAME="${E2E_ADDRESS_NAME:-paas-e2e}"

log() { printf '\n▸ %s\n' "$*" >&2; }

log "резервируем публичный адрес ${ADDRESS_NAME} в ${ZONE}"
if yc vpc address get --name "$ADDRESS_NAME" >/dev/null 2>&1; then
  echo "адрес уже зарезервирован — берём его" >&2
else
  yc vpc address create --name "$ADDRESS_NAME" --external-ipv4 "zone=${ZONE}" >/dev/null
fi
IP="$(yc vpc address get --name "$ADDRESS_NAME" --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["external_ipv4_address"]["address"])')"
echo "адрес: ${IP}" >&2

# Wildcard нужен потому, что имя хоста выкатки складывается из имени сервиса, а
# каждый прогон заводит сервис с новым именем: заранее записи не пропишешь.
log "заводим записи ${SUBDOMAIN}.${DOMAIN} и *.${SUBDOMAIN}.${DOMAIN}"
yc dns zone add-records --name "$DNS_ZONE" \
  --record "${SUBDOMAIN} 300 A ${IP}" \
  --record "*.${SUBDOMAIN} 300 A ${IP}" >/dev/null 2>&1 || \
  echo "записи уже есть — пропускаем" >&2

log "выпускаем корневой сертификат кластера"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# ECDSA P-256 — то же, чем подписывает себя k3s по умолчанию.
openssl ecparam -genkey -name prime256v1 -out "${WORK}/ca.key" 2>/dev/null
openssl req -x509 -new -sha256 -days 3650 \
  -key "${WORK}/ca.key" -out "${WORK}/ca.crt" \
  -subj "/CN=paas-e2e-server-ca" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" 2>/dev/null

PLATFORM_TOKEN="$(openssl rand -hex 32)"
ADMIN_TOKEN="$(openssl rand -hex 32)"

b64() { base64 < "$1" | tr -d '\n'; }

cat <<EOF

Готово. Дальше — руками, по одному разу.

1) Секреты репозитория проверок:

   gh secret set E2E_CLUSTER_HOST  --body '${SUBDOMAIN}.${DOMAIN}'
   gh secret set E2E_CLUSTER_IP    --body '${IP}'
   gh secret set E2E_CA_CRT        --body '$(b64 "${WORK}/ca.crt")'
   gh secret set E2E_CA_KEY        --body '$(b64 "${WORK}/ca.key")'
   gh secret set E2E_PLATFORM_TOKEN --body '${PLATFORM_TOKEN}'
   gh secret set E2E_ADMIN_TOKEN   --body '${ADMIN_TOKEN}'
   gh secret set E2E_SUBNET        --body '<имя подсети: yc vpc subnet list>'
   gh secret set YC_SA_KEY         --body "\$(cat <ключ сервисного аккаунта>.json)"

2) Поднять кластер первый раз и подключить его к платформе от лица
   организации-песочницы — подключение заводится ОДИН раз и переживает
   пересоздание кластера:

   hack/cluster-up.sh
   # адрес   https://${SUBDOMAIN}.${DOMAIN}:6443
   # CA      сертификат выше
   # токен   ${PLATFORM_TOKEN}

   Штатная команда \`paas-cli cluster connect\` здесь не годится: она заводит
   учётную запись в кластере сама и регистрирует НОВОЕ подключение, а нам нужно
   ровно одно, переживающее пересоздание.

3) Назначить подключённому кластеру окружение и завести доменную зону
   ${SUBDOMAIN}.${DOMAIN} в организации-песочнице.

EOF
