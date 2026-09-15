# Общее для подъёма и сноса: init OpenTofu с бэкендом state в Object Storage.
# Подключается через source. Ключи бакета — AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY
# (статический ключ сервисного аккаунта), доступ провайдера — YC_* из окружения.

INFRA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../infra" && pwd)"
E2E_STATE_BUCKET="${E2E_STATE_BUCKET:-traumtech-paas-e2e-state}"

infra_init() {
  : "${AWS_ACCESS_KEY_ID:?ключ бакета state}"
  : "${AWS_SECRET_ACCESS_KEY:?секрет ключа бакета state}"
  tofu -chdir="$INFRA_DIR" init -input=false -reconfigure \
    -backend-config="bucket=${E2E_STATE_BUCKET}" \
    -backend-config="key=paas-e2e/run.tfstate" >/dev/null
}

infra() { tofu -chdir="$INFRA_DIR" "$@"; }
