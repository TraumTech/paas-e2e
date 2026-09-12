package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// Внешние входы платформы: веб-интерфейс, вход в аккаунт, описание API. Ходим
// без токена — так их видит любой пользователь; заодно проверяются DNS,
// сертификат и ingress.
func TestPublicEntrypointsAnswer(t *testing.T) {
	cfg := platform.Config()
	entrypoints := map[string]string{
		"веб-интерфейс":  cfg.WebURL + "/",
		"вход в аккаунт": cfg.AuthURL + "/health/ready",
		"описание API":   cfg.APIURL + "/openapi.json",
	}

	for name, address := range entrypoints {
		t.Run(name, func(t *testing.T) {
			status, err := platform.StatusOf(testContext(t), address, false)
			if err != nil {
				fail(t, name, err)
			}
			if status != http.StatusOK {
				t.Fatalf("%s (%s): ОТКАЗ — %d, ожидался 200", name, address, status)
			}
		})
	}
}

// Данные организации закрыты: запрос без предъявленного доступа не проходит.
// Регрессия здесь — это открытые наружу данные, а не просто сломанный сценарий.
func TestRequestWithoutTokenRefused(t *testing.T) {
	address := platform.Config().APIURL + "/organizations/current"

	status, err := platform.StatusOf(testContext(t), address, false)
	if err != nil {
		fail(t, "запрос без токена", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("запрос без токена вернул %d, ожидался 401 — доступ к данным организации не закрыт", status)
	}
}

func TestOrganizationVisible(t *testing.T) {
	var org Organization
	if err := platform.Get(testContext(t), "/organizations/current", &org); err != nil {
		fail(t, "организация владельца токена", err)
	}
	if org.ID == "" || org.Name == "" {
		t.Fatalf("организация пришла неполной: %+v", org)
	}
	t.Logf("организация: %s (%s)", org.Name, org.ID)
}

func TestProductsListed(t *testing.T) {
	var list ProductList
	if err := platform.Get(testContext(t), "/organizations/current/products", &list); err != nil {
		fail(t, "перечень продуктов", err)
	}
	if len(list.Products) == 0 {
		t.Fatal("у организации не оказалось ни одного продукта")
	}
	for _, product := range list.Products {
		if product.ID == "" || product.Name == "" || product.Stage == "" {
			t.Errorf("продукт пришёл неполным: %+v", product)
		}
	}
	t.Logf("продуктов: %d", len(list.Products))
}

func TestServicesListed(t *testing.T) {
	services := listServices(t)
	for _, service := range services {
		if service.ID == "" || service.Name == "" {
			t.Errorf("сервис пришёл неполным: %+v", service)
		}
	}
	t.Logf("сервисов: %d", len(services))
}

// Версия — след доехавшей сборки: если у сервисов перестали появляться версии,
// линия доставки встала, даже когда все ручки отвечают.
func TestServiceVersionsReadable(t *testing.T) {
	services := listServices(t)
	withVersions := 0

	for _, service := range services {
		t.Run(service.Name, func(t *testing.T) {
			var versions []Version
			if err := platform.Get(testContext(t), "/services/"+service.ID+"/versions", &versions); err != nil {
				fail(t, "версии сервиса "+service.Name, err)
			}
			if len(versions) == 0 {
				t.Skip("версий пока нет")
			}
			withVersions++
			// Образ версия несёт сама (DEP-02) или через сборку (DEP-19) — без
			// того и другого катить нечего.
			for _, version := range versions {
				if version.Number <= 0 || (version.Image == "" && version.BuildID == "") {
					t.Errorf("версия пришла неполной: %+v", version)
				}
			}
			latest := versions[0]
			for _, version := range versions {
				if version.Number > latest.Number {
					latest = version
				}
			}
			t.Logf("последняя версия %d (%s, %s)", latest.Number, latest.Environment, age(latest.CreatedAt))
		})
	}

	if withVersions == 0 {
		t.Fatal("ни у одного сервиса нет версий — линия доставки не оставила следа")
	}
}

// Опубликованный контракт читается целиком и сверяется сам с собой: сверка
// поднимает весь тракт реестра протоколов (разбор формата, сужение, потребители),
// ничего при этом не меняя. Ломающих изменений против самого себя быть не может.
func TestPublishedProtocolsCompatibleWithThemselves(t *testing.T) {
	services := listServices(t)
	checked := 0

	for _, service := range services {
		var list ProtocolList
		if err := platform.Get(testContext(t), "/services/"+service.ID+"/protocols", &list); err != nil {
			t.Run(service.Name, func(t *testing.T) { fail(t, "перечень протоколов "+service.Name, err) })
			continue
		}

		for _, ref := range list.Protocols {
			checked++
			t.Run(service.Name+"/"+ref.Name, func(t *testing.T) {
				query := url.Values{"name": {ref.Name}}
				var protocol Protocol
				if err := platform.Get(testContext(t), "/services/"+service.ID+"/protocol?"+query.Encode(), &protocol); err != nil {
					fail(t, "контракт "+ref.Name, err)
				}
				if !protocol.Published {
					t.Fatalf("контракт %s значится в перечне, но не опубликован", ref.Name)
				}
				if len(protocol.Document) == 0 && protocol.DocumentText == "" {
					t.Fatalf("контракт %s пришёл пустым", ref.Name)
				}

				candidate := candidateOf(protocol)
				if candidate == nil {
					t.Skipf("формат %q сверкой не проверяется", protocol.Format)
				}
				path := fmt.Sprintf("/services/%s/protocol/compatibility?%s", service.ID,
					url.Values{"format": {protocol.Format}, "name": {ref.Name}}.Encode())

				var report CompatibilityReport
				if err := platform.Post(testContext(t), path, candidate, &report); err != nil {
					fail(t, "сверка контракта "+ref.Name, err)
				}
				if report.Breaking {
					t.Fatalf("сверка опубликованного контракта %s с самим собой признана ломающей: %+v", ref.Name, report.Consumers)
				}
				t.Logf("формат %s, версия %d, потребителей %d", protocol.Format, ref.VersionNumber, len(report.Consumers))
			})
		}
	}

	if checked == 0 {
		t.Fatal("ни у одного сервиса нет опубликованного контракта")
	}
}

// Кандидат для сверки предъявляется в том же виде, в каком контракт хранится:
// структурный формат — документом, текстовый — исходником.
func candidateOf(protocol Protocol) any {
	switch {
	case len(protocol.Document) > 0:
		return protocol.Document
	case protocol.DocumentText != "":
		return protocol.DocumentText
	default:
		return nil
	}
}

func listServices(t *testing.T) []Service {
	t.Helper()
	var services []Service
	if err := platform.Get(testContext(t), "/services", &services); err != nil {
		fail(t, "перечень сервисов", err)
	}
	if len(services) == 0 {
		t.Fatal("у организации не оказалось ни одного сервиса")
	}
	return services
}
