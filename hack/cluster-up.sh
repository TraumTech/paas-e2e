#!/usr/bin/env bash
# Поднимает свежий одноузловой кластер под текущий прогон, готовый к выкаткам
# платформы. Облачная часть — адрес, записи DNS, машина — описана в infra/ и
# доводится OpenTofu; настройка внутри кластера запечена в cloud-init, здесь
# только ждём готовности.
#
# Кластер каждый раз новый, но корневой сертификат и токены у него те же — их
# выпускает разово secrets-once.sh. Поэтому подключение кластера на платформе
# заводится один раз и переживает пересоздание.
#
# Использование: hack/cluster-up.sh
# Результат: kubeconfig в $KUBECONFIG_OUT (по умолчанию ./.e2e-kubeconfig).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=infra.sh
source "$HERE/infra.sh"

: "${E2E_CA_CRT:?корневой сертификат кластера, base64}"
: "${E2E_CA_KEY:?ключ корневого сертификата, base64}"
: "${E2E_PLATFORM_TOKEN:?токен, которым платформа ходит в кластер}"
: "${E2E_ADMIN_TOKEN:?токен, которым в кластер ходит сам прогон}"
: "${PAAS_API_TOKEN:?токен платформы — из него берутся требуемые права}"

E2E_CLUSTER_HOST="${E2E_CLUSTER_HOST:-e2e.traumtech.ru}"
E2E_SUBNET="${E2E_SUBNET:-default-ru-central1-a}"
KUBECONFIG_OUT="${KUBECONFIG_OUT:-$PWD/.e2e-kubeconfig}"
API="https://${E2E_CLUSTER_HOST}:6443"

# Версии повторяют то, что стоит в постоянном кластере: проверять выкатку на
# другом контроллере входа смысла нет.
INGRESS_NGINX_VERSION="${E2E_INGRESS_NGINX_VERSION:-controller-v1.12.1}"
CERT_MANAGER_VERSION="${E2E_CERT_MANAGER_VERSION:-v1.16.2}"
ACME_EMAIL="${E2E_ACME_EMAIL:-ruslanbergutov@gmail.com}"

log() { printf '\n▸ %s\n' "$*" >&2; }

# Права платформе — ровно те, что она сама объявляет требуемыми: в cloud-init
# они уезжают манифестом, k3s применит его при старте. Спрашивает и рендерит
# hack/rbac на Go — тем же клиентом, что ходят сценарии.
log "собираем cloud-init: права платформы, контроллер входа, выпуск сертификатов"
PLATFORM_RBAC_B64="$(cd "$HERE/.." && go run ./hack/rbac | base64 | tr -d '\n')"

TF_VAR_user_data="$(sed \
  -e "s|__PLATFORM_TOKEN__|${E2E_PLATFORM_TOKEN}|" \
  -e "s|__ADMIN_TOKEN__|${E2E_ADMIN_TOKEN}|" \
  -e "s|__CA_CRT_B64__|${E2E_CA_CRT}|" \
  -e "s|__CA_KEY_B64__|${E2E_CA_KEY}|" \
  -e "s|__PLATFORM_RBAC_B64__|${PLATFORM_RBAC_B64}|" \
  -e "s|__ACME_EMAIL__|${ACME_EMAIL}|" \
  -e "s|__INGRESS_NGINX_VERSION__|${INGRESS_NGINX_VERSION}|" \
  -e "s|__CERT_MANAGER_VERSION__|${CERT_MANAGER_VERSION}|" \
  -e "s|__HOST__|${E2E_CLUSTER_HOST}|" \
  "$HERE/cloud-init.yaml.tmpl")"
export TF_VAR_user_data
export TF_VAR_host="$E2E_CLUSTER_HOST" TF_VAR_subnet_name="$E2E_SUBNET"
export TF_VAR_address_name="${E2E_ADDRESS_NAME:-paas-e2e}" TF_VAR_vm_name="${E2E_VM_NAME:-paas-e2e}"

# Машина от прошлого прогона могла остаться — снос до подъёма делает скрипт
# повторяемым; адрес и записи DNS при этом доводятся до нужного состояния.
log "доводим облако: адрес, записи DNS, свежая машина"
"$HERE/cluster-down.sh"
infra_init
infra apply -input=false -auto-approve >/dev/null
log "адрес кластера: $(infra output -raw ip)"

log "ждём API-сервер на ${API}"
CA_FILE="$(mktemp)"
trap 'rm -f "$CA_FILE"' EXIT
printf '%s' "$E2E_CA_CRT" | base64 -d > "$CA_FILE"

deadline=$(( $(date +%s) + 600 ))
until curl -sf --max-time 5 --cacert "$CA_FILE" \
      -H "Authorization: Bearer ${E2E_ADMIN_TOKEN}" "${API}/version" >/dev/null; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "кластер не поднялся за 10 минут: ${API} не отвечает" >&2
    exit 1
  fi
  sleep 10
done
log "API-сервер отвечает"

log "пишем kubeconfig в ${KUBECONFIG_OUT}"
cat > "$KUBECONFIG_OUT" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: paas-e2e
    cluster:
      server: ${API}
      certificate-authority-data: ${E2E_CA_CRT}
users:
  - name: e2e-admin
    user:
      token: ${E2E_ADMIN_TOKEN}
contexts:
  - name: paas-e2e
    context:
      cluster: paas-e2e
      user: e2e-admin
current-context: paas-e2e
EOF
chmod 600 "$KUBECONFIG_OUT"
export KUBECONFIG="$KUBECONFIG_OUT"

# «Поднялся» и «готов» — разные состояния: манифесты из cloud-init k3s
# применяет уже после старта API, здесь ждём результата.
log "ждём готовности узла, контроллера входа и cert-manager"
kubectl wait --for=condition=Ready node --all --timeout=5m
deadline=$(( $(date +%s) + 300 ))
until kubectl -n ingress-nginx get deploy/ingress-nginx-controller >/dev/null 2>&1 \
   && kubectl -n cert-manager get deploy/cert-manager-webhook >/dev/null 2>&1; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "поднялся, но не готов: манифесты из cloud-init не применились за 5 минут" >&2
    exit 1
  fi
  sleep 10
done
kubectl -n ingress-nginx wait --for=condition=Available deploy/ingress-nginx-controller --timeout=5m
kubectl -n cert-manager wait --for=condition=Available deploy --all --timeout=5m
kubectl wait --for=condition=Ready clusterissuer/letsencrypt-prod --timeout=3m

log "кластер готов: ${API}"
