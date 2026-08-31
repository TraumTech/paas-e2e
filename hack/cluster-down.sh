#!/usr/bin/env bash
# Сносит машину с кластером прогона. Всё, что платформа успела в него выкатить,
# исчезает вместе с ней — другого способа снять выкатку у нас пока нет.
#
# Идемпотентен: машины нет — молча выходим. Поэтому его безопасно звать и перед
# созданием, и в конце прогона, в том числе после падения посередине.
set -euo pipefail

VM_NAME="${E2E_VM_NAME:-paas-e2e}"

if ! yc compute instance get --name "$VM_NAME" >/dev/null 2>&1; then
  exit 0
fi

yc compute instance delete --name "$VM_NAME" >/dev/null
echo "машина ${VM_NAME} снесена" >&2
