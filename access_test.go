//go:build cluster

package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Образ пробы отвечает на любой путь и печатает заголовки запроса: по ним
// видно, что запрос прошёл через прокси личности.
const accessProbeImage = "traefik/whoami:v1.10.3"

// Правила доступа держатся и в обход публичного адреса (TRA-518/AC-11):
// запрос приходит на Service процесса изнутри кластера — через API кластера,
// минуя вход, — и всё равно попадает в прокси личности.
func TestAccessRulesHoldInsideCluster(t *testing.T) {
	ensurePreconditions(t)
	environment := envOr("E2E_ENVIRONMENT", "dev")
	refuseUnlessRunCluster(t, environment)

	name := disposablePrefix + "access-" + time.Now().UTC().Format("20060102-150405")
	var service Service
	err := platform.Post(contextFor(t, 3*time.Minute), "/services",
		map[string]string{"name": name, "product_id": anyProduct(t).ID}, &service)
	if err != nil {
		fail(t, "создание сервиса "+name, err)
	}
	t.Cleanup(func() { removeService(t, service) })

	// Сборку публикуем сами, не дожидаясь CI заготовки: нужен процесс с
	// правилами доступа, а не её содержимое.
	const process, port = "server", 8080
	form := map[string]any{"processes": []any{map[string]any{
		"name": process, "command": []string{"/whoami", "--port", "8080"}, "listen_port": port,
		"cpu": "50m", "memory": "64Mi",
		"access": []any{
			map[string]any{"paths": []string{"/api/**"}, "sources": []string{"paas"}},
			map[string]any{"paths": []string{"/docs"}, "methods": []string{"GET"}, "anonymous": true},
		},
	}}}
	var build Build
	err = platform.Post(testContext(t), "/services/"+service.ID+"/builds", map[string]any{
		"commit_revision": strings.Repeat("0", 26) + time.Now().UTC().Format("20060102150405"),
		"branch":          "main", "image": accessProbeImage, "form": form,
	}, &build)
	if err != nil {
		fail(t, "публикация сборки с правилами доступа", err)
	}

	var deployment Deployment
	err = platform.Post(contextFor(t, 3*time.Minute),
		"/organizations/current/environments/"+url.PathEscape(environment)+"/deployments",
		map[string]string{"service_id": service.ID, "build_id": build.ID}, &deployment)
	if err != nil {
		fail(t, "выкатка сборки с правилами доступа в окружение "+environment, err)
	}

	statusPath := "/organizations/current/environments/" + url.PathEscape(environment) +
		"/services/" + service.ID + "/status"
	waitUntil(t, "готовность процесса с прокси личности", 6*time.Minute, func() (bool, error) {
		var status WorkloadStatus
		if err := platform.Get(testContext(t), statusPath, &status); err != nil {
			return false, err
		}
		return status.Deployed && len(status.Processes) == 1 && status.Processes[0].ReadyReplicas >= 1, nil
	})

	// API кластера ходит на Service сам, как сосед по кластеру; свой токен он
	// процессу не передаёт, поэтому запрос приходит без личности.
	kube := adminAccess(t)
	inside := "/api/v1/namespaces/" + deployment.Namespace + "/services/" + process + ":listen/proxy"

	status, body, err := kube.get(testContext(t), inside+"/docs")
	if err != nil {
		fail(t, "открытый путь изнутри кластера", err)
	}
	if status != http.StatusOK || !strings.Contains(string(body), "X-Identity-Source: anonymous") {
		t.Fatalf("открытый путь изнутри кластера: код %d, процесс не получил пометку анонима — запрос прошёл мимо прокси:\n%s", status, body)
	}

	for path, want := range map[string]int{
		"/api/items": http.StatusUnauthorized,
		"/other":     http.StatusNotFound,
	} {
		status, body, err := kube.get(testContext(t), inside+path)
		if err != nil {
			fail(t, "путь "+path+" изнутри кластера", err)
		}
		if status != want {
			t.Errorf("путь %s изнутри кластера: код %d вместо %d — правила доступа обойдены:\n%s", path, status, want, body)
		}
	}
}
