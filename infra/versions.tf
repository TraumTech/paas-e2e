# Облачная часть кластера прогона: адрес, записи DNS, машина. Описана
# декларативно — идемпотентность и уборка достаются от OpenTofu, а не пишутся
# руками в скрипте. Настройка внутри кластера сюда не входит: она запечена в
# cloud-init машины (k3s применяет манифесты из своего каталога при старте).
terraform {
  required_version = ">= 1.8"

  required_providers {
    yandex = {
      source  = "yandex-cloud/yandex"
      version = ">= 0.120"
    }
  }

  # State живёт в Object Storage между прогонами: адрес и записи постоянные, а
  # машина каждый раз новая — без state прогон не знал бы, что осталось от
  # прошлого. Бакет и ключи приходят при init (hack/infra.sh); блокировок нет —
  # прогоны и так сериализованы concurrency-группой workflow.
  backend "s3" {
    endpoints = { s3 = "https://storage.yandexcloud.net" }
    region    = "ru-central1"

    skip_region_validation      = true
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
  }
}

# Доступ провайдера — из окружения: YC_SERVICE_ACCOUNT_KEY_FILE, YC_CLOUD_ID,
# YC_FOLDER_ID.
provider "yandex" {
  zone = var.zone
}
