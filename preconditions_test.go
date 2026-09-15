//go:build cluster

package e2e

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
)

// Предусловия прогона: что платформа должна знать о песочнице и кластере
// прогона, чтобы сценарии имели смысл. Прогон обеспечивает их сам и каждый раз —
// сделанное узнаёт и пропускает, недостающее доводит; так нет дрейфа между
// «подготовили когда-то» и «что ожидают сценарии», а падение на предусловии
// называется в том же отчёте. Снаружи приходят только реквизиты: они нужны
// прогону для старта, а не порождаются им (README, «Реквизиты»).
type precondition struct {
	name   string
	ensure func() (string, error)
}

var (
	preconditionsOnce sync.Once
	preconditionsErr  error
)

func TestPreconditionsEnsured(t *testing.T) { ensurePreconditions(t) }

// ensurePreconditions доводит предусловия один раз на прогон: сценарии,
// которым они нужны, зовут её сами и не зависят от порядка запуска.
func ensurePreconditions(t *testing.T) {
	t.Helper()
	preconditionsOnce.Do(func() {
		for _, p := range preconditions() {
			state, err := p.ensure()
			if err != nil {
				preconditionsErr = fmt.Errorf("предусловие «%s»: %w", p.name, err)
				return
			}
			t.Logf("%s: %s", p.name, state)
		}
	})
	if preconditionsErr != nil {
		fail(t, "предусловия прогона", preconditionsErr)
	}
}

func preconditions() []precondition {
	environment := envOr("E2E_ENVIRONMENT", "dev")
	clusterName := envOr("E2E_CLUSTER_NAME", "paas-e2e")
	zoneSlug := envOr("E2E_ZONE_SLUG", "e2e")
	host := clusterHost()

	return []precondition{
		{"продукт для одноразовых сервисов", func() (string, error) {
			// Без продукта сервис не создаётся, а у свежей организации продуктов нет.
			var list ProductList
			if err := platform.Get(background(), "/organizations/current/products", &list); err != nil {
				return "", err
			}
			if len(list.Products) > 0 {
				return "есть: " + list.Products[0].Name, nil
			}
			if err := platform.Post(background(), "/organizations/current/products", map[string]string{"name": "e2e"}, nil); err != nil {
				return "", err
			}
			return "заведён e2e", nil
		}},

		{"подключение хостинга кода", func() (string, error) {
			var list ScmConnectionList
			if err := platform.Get(background(), "/organizations/current/scm-connections", &list); err != nil {
				return "", err
			}
			if len(list.Connections) > 0 {
				c := list.Connections[0]
				return c.AccountType + " " + c.AccountLogin, nil
			}
			// Установить приложение в аккаунт песочницы без браузера нельзя —
			// это реквизит; завершить подключение по известной установке можно.
			installation := os.Getenv("E2E_GITHUB_INSTALLATION_ID")
			if installation == "" {
				return "", errors.New("подключения нет и не задан E2E_GITHUB_INSTALLATION_ID: установите GitHub App платформы в аккаунт песочницы и положите id установки в секрет")
			}
			var intent InstallIntent
			if err := platform.Post(background(), "/organizations/current/scm-connections/install-intent", nil, &intent); err != nil {
				return "", err
			}
			u, err := url.Parse(intent.InstallURL)
			if err != nil || u.Query().Get("state") == "" {
				return "", fmt.Errorf("адрес установки без нонса: %q", intent.InstallURL)
			}
			var c ScmConnection
			body := map[string]string{"installation_id": installation, "state": u.Query().Get("state")}
			if err := platform.Post(background(), "/organizations/current/scm-connections", body, &c); err != nil {
				return "", err
			}
			return "завершено: " + c.AccountType + " " + c.AccountLogin, nil
		}},

		{"креденшел реестра ghcr.io", func() (string, error) {
			// Кластер тянет приватный образ заготовки: без креденшела выкатка
			// зависнет на pull. Заданный реквизит кладём каждый раз — замена штатна.
			user, token := os.Getenv("E2E_GHCR_USER"), os.Getenv("E2E_GHCR_TOKEN")
			if token != "" {
				body := map[string]string{"username": user, "token": token}
				if err := platform.Put(background(), "/organizations/current/registry-credentials/ghcr.io", body, nil); err != nil {
					return "", err
				}
				return "записан для " + user, nil
			}
			var list RegistryCredentialList
			if err := platform.Get(background(), "/organizations/current/registry-credentials", &list); err != nil {
				return "", err
			}
			for _, c := range list.Credentials {
				if c.Server == "ghcr.io" {
					return "есть: " + c.Username, nil
				}
			}
			return "", errors.New("креденшела ghcr.io нет и не заданы E2E_GHCR_USER/E2E_GHCR_TOKEN")
		}},

		{"подключение кластера прогона", func() (string, error) {
			// Кластер каждый раз новый, реквизиты те же: запись заводится однажды и
			// переживает пересоздание — отзывать подключения платформа не умеет.
			if c := findCluster(clusterName); c != nil {
				return "есть (" + age(c.LastContactAt) + ")", nil
			}
			ca, err := base64.StdEncoding.DecodeString(os.Getenv("E2E_CA_CRT"))
			if err != nil || len(ca) == 0 || os.Getenv("E2E_PLATFORM_TOKEN") == "" {
				return "", errors.New("подключения нет, а реквизиты E2E_CA_CRT/E2E_PLATFORM_TOKEN не заданы")
			}
			body := map[string]string{
				"name": clusterName, "topology": "zonal",
				"endpoint":       "https://" + host + ":6443",
				"ca_certificate": string(ca),
				"token":          os.Getenv("E2E_PLATFORM_TOKEN"),
			}
			if err := platform.Post(background(), "/organizations/current/clusters", body, nil); err != nil {
				return "", err
			}
			return "подключён", nil
		}},

		{"кластер обслуживает окружение " + environment, func() (string, error) {
			c := findCluster(clusterName)
			if c == nil {
				return "", errors.New("кластера нет в перечне организации")
			}
			if c.Environment == environment {
				return "да", nil
			}
			body := map[string]string{"cluster_id": c.ID}
			if err := platform.Put(background(), "/organizations/current/environments/"+url.PathEscape(environment)+"/cluster", body, nil); err != nil {
				return "", err
			}
			return "назначен", nil
		}},

		{"доменная зона " + zoneSlug + " с базовым хостом " + host, func() (string, error) {
			var list DomainZoneList
			if err := platform.Get(background(), "/organizations/current/domain-zones", &list); err != nil {
				return "", err
			}
			var zone *DomainZone
			for i := range list.Zones {
				if list.Zones[i].Slug == zoneSlug {
					zone = &list.Zones[i]
				}
			}
			if zone == nil {
				body := map[string]string{"slug": zoneSlug, "name": "Кластер прогона"}
				if err := platform.Post(background(), "/organizations/current/domain-zones", body, nil); err != nil {
					return "", err
				}
				zone = &DomainZone{Slug: zoneSlug}
			}
			for _, h := range zone.Hosts {
				if h.Environment == environment && h.BaseHost == host {
					return "есть", nil
				}
			}
			path := "/organizations/current/domain-zones/" + url.PathEscape(zoneSlug) + "/hosts/" + url.PathEscape(environment)
			if err := platform.Put(background(), path, map[string]string{"base_host": host}, nil); err != nil {
				return "", err
			}
			return "задана", nil
		}},
	}
}

func findCluster(name string) *Cluster {
	var list ClusterList
	if err := platform.Get(background(), "/organizations/current/clusters", &list); err != nil {
		return nil
	}
	for i := range list.Clusters {
		if list.Clusters[i].Name == name {
			return &list.Clusters[i]
		}
	}
	return nil
}
