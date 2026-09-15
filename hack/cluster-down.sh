#!/usr/bin/env bash
# Сносит машину с кластером прогона. Всё, что платформа успела в него выкатить,
# исчезает вместе с ней — другого способа снять выкатку у нас пока нет. Адрес
# и записи DNS остаются: они постоянные, под них записано подключение.
#
# Идемпотентен: машины нет в state — OpenTofu ничего не делает. Поэтому его
# безопасно звать и перед подъёмом, и в конце прогона, в том числе после
# падения посередине.
set -euo pipefail
# shellcheck source=infra.sh
source "$(dirname "$0")/infra.sh"

infra_init
infra destroy -input=false -auto-approve -target=yandex_compute_instance.run >/dev/null
echo "машина кластера прогона снесена (если была)" >&2
