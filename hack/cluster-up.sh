#!/usr/bin/env bash
# Поднимает свежий одноузловой кластер под текущий прогон и готовит его к
# выкаткам платформы: контроллер входа, выпуск сертификатов, права платформы.
#
# Кластер каждый раз новый, но адрес, корневой сертификат и токены у него те же
# — их выпускает разово secrets-once.sh. Поэтому подключение
# кластера на платформе заводится один раз и переживает пересоздание.
#
# Использование: hack/cluster-up.sh
# Результат: kubeconfig в $KUBECONFIG_OUT (по умолчанию ./.e2e-kubeconfig).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

: "${E2E_CA_CRT:?корневой сертификат кластера, base64}"
: "${E2E_CA_KEY:?ключ корневого сертификата, base64}"
: "${E2E_PLATFORM_TOKEN:?токен, которым платформа ходит в кластер}"
: "${E2E_ADMIN_TOKEN:?токен, которым в кластер ходит сам прогон}"
: "${E2E_SUBNET:?имя или идентификатор подсети}"
: "${PAAS_API_URL:=https://api.paas.traumtech.ru}"
: "${PAAS_API_TOKEN:?токен платформы — из него берутся требуемые права}"

VM_NAME="${E2E_VM_NAME:-paas-e2e}"
ZONE="${E2E_ZONE:-ru-central1-a}"
KUBECONFIG_OUT="${KUBECONFIG_OUT:-$PWD/.e2e-kubeconfig}"
E2E_CLUSTER_HOST="${E2E_CLUSTER_HOST:-e2e.traumtech.ru}"
ADDRESS_NAME="${E2E_ADDRESS_NAME:-paas-e2e}"
DNS_ZONE="${E2E_DNS_ZONE:-traumtech-ru}"
DNS_DOMAIN="${E2E_DNS_DOMAIN:-traumtech.ru}"
API="https://${E2E_CLUSTER_HOST}:6443"

# Версии повторяют то, что стоит в постоянном кластере: проверять выкатку на
# другом контроллере входа смысла нет.
INGRESS_NGINX_VERSION="${E2E_INGRESS_NGINX_VERSION:-controller-v1.12.1}"
CERT_MANAGER_VERSION="${E2E_CERT_MANAGER_VERSION:-v1.16.2}"
ACME_EMAIL="${E2E_ACME_EMAIL:-ruslanbergutov@gmail.com}"

log() { printf '\n▸ %s\n' "$*" >&2; }

# Прошлый прогон мог упасть, не убрав за собой: адрес занят, и создание
# упало бы. Снос до создания делает скрипт повторяемым.
log "убираем машину, оставшуюся от прошлого прогона (если есть)"
"$HERE/cluster-down.sh"

# Адрес постоянный — под него записаны DNS и подключение на платформе, — но
# резервирует и прописывает его сам подъём: сделанное узнаёт и пропускает.
log "адрес ${ADDRESS_NAME} и записи DNS для ${E2E_CLUSTER_HOST}"
if ! yc vpc address get --name "$ADDRESS_NAME" >/dev/null 2>&1; then
  yc vpc address create --name "$ADDRESS_NAME" --external-ipv4 "zone=${ZONE}" >/dev/null
fi
E2E_CLUSTER_IP="$(yc vpc address get --name "$ADDRESS_NAME" --format json \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["external_ipv4_address"]["address"])')"
# Wildcard нужен: хост выкатки складывается из имени сервиса, а он каждый
# прогон новый. Общий *.${DNS_DOMAIN} смотрит на вход постоянного кластера,
# поэтому записи поддомена заводятся явно; replace — идемпотентно.
SUBDOMAIN="${E2E_CLUSTER_HOST%.${DNS_DOMAIN}}"
yc dns zone replace-records --name "$DNS_ZONE" \
  --record "${SUBDOMAIN} 300 A ${E2E_CLUSTER_IP}" \
  --record "*.${SUBDOMAIN} 300 A ${E2E_CLUSTER_IP}" >/dev/null

log "создаём машину ${VM_NAME} в ${ZONE} с адресом ${E2E_CLUSTER_IP}"
CLOUD_INIT="$(mktemp)"
trap 'rm -f "$CLOUD_INIT"' EXIT
sed \
  -e "s|__PLATFORM_TOKEN__|${E2E_PLATFORM_TOKEN}|" \
  -e "s|__ADMIN_TOKEN__|${E2E_ADMIN_TOKEN}|" \
  -e "s|__CA_CRT_B64__|${E2E_CA_CRT}|" \
  -e "s|__CA_KEY_B64__|${E2E_CA_KEY}|" \
  -e "s|__HOST__|${E2E_CLUSTER_HOST}|" \
  -e "s|__IP__|${E2E_CLUSTER_IP}|" \
  "$HERE/cloud-init.yaml.tmpl" > "$CLOUD_INIT"

# Прерываемая машина: кластер живёт минуты, вытеснение за это время
# маловероятно, а платить полную цену за одноразовый узел незачем.
yc compute instance create \
  --name "$VM_NAME" \
  --zone "$ZONE" \
  --preemptible \
  --cores 4 --memory 8GB --core-fraction 100 \
  --create-boot-disk image-folder-id=standard-images,image-family=ubuntu-2404-lts,size=30GB,type=network-ssd \
  --network-interface "subnet-name=${E2E_SUBNET},nat-ip-version=ipv4,nat-address=${E2E_CLUSTER_IP}" \
  --metadata-from-file user-data="$CLOUD_INIT" \
  --async >/dev/null

log "ждём API-сервер на ${API}"
CA_FILE="$(mktemp)"
trap 'rm -f "$CLOUD_INIT" "$CA_FILE"' EXIT
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

kubectl wait --for=condition=Ready node --all --timeout=5m

# Права платформе выдаём ровно те, что она сама объявляет требуемыми. Если она
# начнёт ходить за большим, чем объявила, прогон это увидит — на постоянном
# кластере такое расхождение прячется за давно выданной ролью.
log "выдаём платформе объявленные ею права"
curl -sf --max-time 20 -H "Authorization: Bearer ${PAAS_API_TOKEN}" \
  "${PAAS_API_URL}/organizations/current/clusters/required-access" \
| python3 "$HERE/required_access_to_rbac.py" \
| kubectl apply -f -

log "ставим контроллер входа ingress-nginx ${INGRESS_NGINX_VERSION}"
kubectl apply -f "https://raw.githubusercontent.com/kubernetes/ingress-nginx/${INGRESS_NGINX_VERSION}/deploy/static/provider/cloud/deploy.yaml"

log "ставим cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

kubectl -n ingress-nginx wait --for=condition=Available deploy/ingress-nginx-controller --timeout=5m
kubectl -n cert-manager wait --for=condition=Available deploy --all --timeout=5m

# Имя выпускающего задано платформой: она проставляет его аннотацией на каждом
# сгенерированном Ingress. Назвать иначе — значит не выпустить ни одного
# сертификата.
log "заводим выпускающего letsencrypt-prod"
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ${ACME_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - http01:
          ingress:
            class: nginx
EOF

log "кластер готов: ${API}"
