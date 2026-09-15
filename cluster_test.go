//go:build cluster

package e2e

import (
	"net/http"
	"os"
	"testing"
)

// Кластер поднят заново, но предъявляет вчерашние корневой сертификат и токен.
// Это и есть трюк, на котором держится вся схема: подключение кластера на
// платформе заводится один раз и переживает пересоздание. Разъехалось —
// дальше проверять нечего, платформа в такой кластер не войдёт.
func TestClusterAnswersWithPinnedCredentials(t *testing.T) {
	kube := adminAccess(t)

	var version struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := kube.Get(testContext(t), "/version", &version); err != nil {
		fail(t, "свежий кластер по закреплённому доступу", err)
	}
	if version.GitVersion == "" {
		t.Fatal("кластер ответил, но не назвал свою версию")
	}
	t.Logf("кластер: %s", version.GitVersion)
}

// Платформа входит в кластер тем же токеном и в тех же правах, что объявила
// требуемыми. Если она начнёт ходить за большим, чем объявляет, это увидим
// здесь: на постоянном кластере расхождение прячется за давно выданной ролью.
func TestPlatformAccessGrantedAsDeclared(t *testing.T) {
	kube, err := KubeConfigFromEnv(os.Getenv("E2E_PLATFORM_TOKEN"))
	if err != nil {
		t.Fatalf("доступ платформы к кластеру: %v", err)
	}

	status, err := kube.StatusOf(testContext(t), "/api/v1/namespaces")
	if err != nil {
		fail(t, "перечень namespace'ов токеном платформы", err)
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		t.Fatal("кластер не принял токен платформы — подключение на платформе разъехалось с кластером")
	case http.StatusForbidden:
		t.Fatal("кластер не дал платформе перечень namespace'ов: выданные права уже́ объявленных")
	default:
		t.Fatalf("неожиданный ответ на перечень namespace'ов: %d", status)
	}
}

// Выкатке платформы нужны контроллер входа и выпуск сертификатов: без них
// сгенерированный Ingress останется без адреса и без сертификата, а сам сбой
// вылезет уже в сценарии выкатки — глубоко и непонятно.
func TestClusterReadyForDeployments(t *testing.T) {
	kube := adminAccess(t)

	deployments := map[string]string{
		"контроллер входа":       "/apis/apps/v1/namespaces/ingress-nginx/deployments/ingress-nginx-controller",
		"выпуск сертификатов":    "/apis/apps/v1/namespaces/cert-manager/deployments/cert-manager",
		"приём вебхуков выпуска": "/apis/apps/v1/namespaces/cert-manager/deployments/cert-manager-webhook",
	}
	for name, path := range deployments {
		t.Run(name, func(t *testing.T) {
			var deployment conditioned
			if err := kube.Get(testContext(t), path, &deployment); err != nil {
				fail(t, name, err)
			}
			status, reason, message, found := deployment.condition("Available")
			if !found {
				t.Fatalf("%s: развёрнут, но о готовности молчит", name)
			}
			if status != "True" {
				t.Fatalf("%s не готов: %s — %s", name, reason, message)
			}
		})
	}

	// Имя выпускающего задаёт платформа аннотацией на каждом Ingress: назван
	// иначе — ни один сертификат не выпустится.
	t.Run("выпускающий letsencrypt-prod", func(t *testing.T) {
		var issuer conditioned
		if err := kube.Get(testContext(t), "/apis/cert-manager.io/v1/clusterissuers/letsencrypt-prod", &issuer); err != nil {
			fail(t, "выпускающий letsencrypt-prod", err)
		}
		status, reason, message, found := issuer.condition("Ready")
		if !found || status != "True" {
			t.Fatalf("выпускающий не готов: %s — %s", reason, message)
		}
	})
}

// Платформа по-прежнему числит этот кластер своим и подключённым: запись
// заводится один раз, а кластер под ней меняется каждый прогон.
func TestPlatformStillHoldsClusterConnection(t *testing.T) {
	ensurePreconditions(t)
	name := envOr("E2E_CLUSTER_NAME", "paas-e2e")

	var list ClusterList
	if err := platform.Get(testContext(t), "/organizations/current/clusters", &list); err != nil {
		fail(t, "перечень кластеров", err)
	}
	for _, cluster := range list.Clusters {
		if cluster.Name != name {
			continue
		}
		if !cluster.Connected {
			t.Fatalf("платформа числит кластер %s неподключённым (последний контакт %s)", name, age(cluster.LastContactAt))
		}
		t.Logf("окружение %s, последний контакт %s", cluster.Environment, age(cluster.LastContactAt))
		return
	}
	t.Fatalf("кластера %s нет в перечне организации — подключение не заведено или удалено", name)
}

func adminAccess(t *testing.T) *Kube {
	t.Helper()
	kube, err := KubeConfigFromEnv(os.Getenv("E2E_ADMIN_TOKEN"))
	if err != nil {
		t.Fatalf("доступ прогона к кластеру: %v", err)
	}
	return kube
}
