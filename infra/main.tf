variable "zone" {
  default = "ru-central1-a"
}

variable "subnet_name" {
  default = "default-ru-central1-a"
}

variable "address_name" {
  default = "paas-e2e"
}

variable "vm_name" {
  default = "paas-e2e"
}

# Имя кластера прогона в DNS; под ним платформа знает кластер, а выкатки
# получают хосты `<сервис>.<host>`.
variable "host" {
  default = "e2e.traumtech.ru"
}

variable "dns_zone_name" {
  default = "traumtech-ru"
}

# cloud-init машины; пустой допустим только для destroy.
variable "user_data" {
  type      = string
  default   = ""
  sensitive = true
}

data "yandex_vpc_subnet" "run" {
  name = var.subnet_name
}

data "yandex_dns_zone" "domain" {
  name = var.dns_zone_name
}

data "yandex_compute_image" "ubuntu" {
  family = "ubuntu-2404-lts"
}

# Адрес постоянный: под него записаны DNS и подключение на платформе.
resource "yandex_vpc_address" "run" {
  name = var.address_name

  external_ipv4_address {
    zone_id = var.zone
  }
}

locals {
  ip = yandex_vpc_address.run.external_ipv4_address[0].address
}

# Wildcard нужен: хост выкатки складывается из имени сервиса, а он каждый
# прогон новый. Общий `*.traumtech.ru` смотрит на вход постоянного кластера,
# поэтому записи поддомена явные.
resource "yandex_dns_recordset" "host" {
  zone_id = data.yandex_dns_zone.domain.id
  name    = "${var.host}."
  type    = "A"
  ttl     = 300
  data    = [local.ip]
}

resource "yandex_dns_recordset" "wildcard" {
  zone_id = data.yandex_dns_zone.domain.id
  name    = "*.${var.host}."
  type    = "A"
  ttl     = 300
  data    = [local.ip]
}

# Машина каждый прогон новая (снос перед подъёмом и в конце). Прерываемая:
# кластер живёт минуты, вытеснение за это время маловероятно, а платить полную
# цену за одноразовый узел незачем.
resource "yandex_compute_instance" "run" {
  name        = var.vm_name
  zone        = var.zone
  platform_id = "standard-v3"

  resources {
    cores         = 4
    memory        = 8
    core_fraction = 100
  }

  boot_disk {
    initialize_params {
      image_id = data.yandex_compute_image.ubuntu.id
      size     = 30
      type     = "network-ssd"
    }
  }

  network_interface {
    subnet_id      = data.yandex_vpc_subnet.run.id
    nat            = true
    nat_ip_address = local.ip
  }

  scheduling_policy {
    preemptible = true
  }

  metadata = {
    user-data = var.user_data
  }
}

output "ip" {
  value = local.ip
}
