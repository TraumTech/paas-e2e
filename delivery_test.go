//go:build cluster

package e2e

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Имена одноразовых сервисов прогона: по префиксу их находит и убирает
// следующий прогон, если этот упал, не дойдя до уборки.
const disposablePrefix = "e2e-"

// Полный путь линии доставки на одноразовом сервисе-болванке (E2E-02):
// платформа заводит сервис вместе с репозиторием и заготовкой, CI заготовки
// публикует сборку, выкатка везёт её в кластер прогона, платформа сообщает о
// готовности процесса — и сервис удаляется вместе с репозиторием. Каждый шаг
// назван, чтобы падение говорило, где именно встала линия.
//
// Идёт только в организации-песочнице: сервис создаётся, и выкатка идёт в
// кластер прогона, который вместе со всем содержимым сносится в конце.
func TestFullDeliveryPath(t *testing.T) {
	environment := envOr("E2E_ENVIRONMENT", "dev")
	refuseUnlessRunCluster(t, environment)
	sweepLeftovers(t)

	name := disposablePrefix + time.Now().UTC().Format("20060102-150405")

	// Создание — сага из четырёх шагов (регистрация, репозиторий, доступ CI,
	// продукт); отказ приходит с названием сорвавшегося шага в теле.
	var service Service
	err := platform.Post(contextFor(t, 3*time.Minute), "/services", map[string]string{"name": name}, &service)
	if err != nil {
		fail(t, "создание сервиса "+name+" с репозиторием", err)
	}
	t.Cleanup(func() { removeService(t, service) })
	if service.ID == "" || service.RepoURL == "" {
		t.Fatalf("сервис создан неполным: %+v", service)
	}
	t.Logf("сервис %s создан, репозиторий %s", service.Name, service.RepoURL)

	// Сборку публикует CI репозитория токеном, который платформа положила туда
	// сама (REPO-07): ждём чужой конвейер, обычно это несколько минут.
	var build Build
	waitUntil(t, "сборка из CI репозитория", 12*time.Minute, func() (bool, error) {
		var list BuildList
		if err := platform.Get(testContext(t), "/services/"+service.ID+"/builds?limit=1", &list); err != nil {
			return false, err
		}
		if len(list.Builds) == 0 {
			return false, nil
		}
		build = list.Builds[0]
		return true, nil
	})
	if build.Image == "" || build.CommitRevision == "" {
		t.Fatalf("сборка пришла неполной: %+v", build)
	}
	t.Logf("сборка %s: ветка %s, образ %s", build.ID, build.Branch, build.Image)

	var deployment Deployment
	err = platform.Post(contextFor(t, 3*time.Minute),
		"/organizations/current/environments/"+url.PathEscape(environment)+"/deployments",
		map[string]string{"service_id": service.ID, "build_id": build.ID}, &deployment)
	if err != nil {
		fail(t, "выкатка сборки в окружение "+environment, err)
	}
	t.Logf("выкачено в кластер %s, namespace %s", deployment.ClusterName, deployment.Namespace)

	statusPath := "/organizations/current/environments/" + url.PathEscape(environment) +
		"/services/" + service.ID + "/status"
	var status WorkloadStatus
	waitUntil(t, "готовность процесса в кластере", 6*time.Minute, func() (bool, error) {
		if err := platform.Get(testContext(t), statusPath, &status); err != nil {
			return false, err
		}
		if !status.Deployed || len(status.Processes) == 0 {
			return false, nil
		}
		for _, process := range status.Processes {
			if process.ReadyReplicas < 1 {
				return false, nil
			}
		}
		return true, nil
	})
	for _, process := range status.Processes {
		t.Logf("процесс %s: образ %s, готово %d из %d", process.Name, process.Image, process.ReadyReplicas, process.DesiredReplicas)
	}

	// Выкатка сборки заводит версию в окружении сама (DEP-19) — это след, по
	// которому неразрушающий набор судит о линии доставки.
	var versions []Version
	if err := platform.Get(testContext(t), "/services/"+service.ID+"/versions", &versions); err != nil {
		fail(t, "версии сервиса после выкатки", err)
	}
	found := false
	for _, version := range versions {
		if version.Environment == environment && version.CommitRevision == build.CommitRevision {
			found = true
			t.Logf("версия %d заведена выкаткой в %s", version.Number, environment)
		}
	}
	if !found {
		t.Fatalf("выкатка не оставила версии в окружении %s: %+v", environment, versions)
	}
}

// Кластер окружения обязан быть кластером прогона: иначе болванка уедет в
// чей-то настоящий кластер, а снять выкатку платформа не умеет.
func refuseUnlessRunCluster(t *testing.T, environment string) {
	t.Helper()
	expected := envOr("E2E_CLUSTER_NAME", "paas-e2e")
	for _, cluster := range listClusters(t) {
		if cluster.Environment != environment {
			continue
		}
		if cluster.Name != expected {
			t.Fatalf("окружение %s обслуживает кластер %s, а не кластер прогона %s — выкатывать туда нельзя",
				environment, cluster.Name, expected)
		}
		return
	}
	t.Fatalf("окружению %s не назначен кластер — выкатывать некуда", environment)
}

// Следы прошлого прогона, упавшего до уборки: одноразовые сервисы по префиксу.
// Их нагрузка уже исчезла вместе с тем кластером; здесь убираем записи
// платформы и репозитории, чтобы следующий прогон начинался с чистого места.
func sweepLeftovers(t *testing.T) {
	t.Helper()
	var services []Service
	if err := platform.Get(testContext(t), "/services", &services); err != nil {
		fail(t, "перечень сервисов перед прогоном", err)
	}
	for _, service := range services {
		if !strings.HasPrefix(service.Name, disposablePrefix) {
			continue
		}
		t.Logf("убираем след прошлого прогона: сервис %s", service.Name)
		removeService(t, service)
	}
}

// Уборка — тоже проверяемый шаг: платформа удаляет сервис вместе с
// репозиторием (REPO-06), и после этого его не должно быть.
func removeService(t *testing.T, service Service) {
	t.Helper()
	path := "/services/" + service.ID
	if err := platform.Delete(contextFor(t, 2*time.Minute), path+"?with_repository=true"); err != nil {
		var refused *RefusedError
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			return
		}
		t.Errorf("удаление сервиса %s вместе с репозиторием: %v", service.Name, err)
		return
	}
	err := platform.Get(testContext(t), path, new(Service))
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Errorf("сервис %s после удаления всё ещё отвечает: %v", service.Name, err)
		return
	}
	t.Logf("сервис %s удалён вместе с репозиторием", service.Name)
}

// waitUntil опрашивает платформу, пока probe не скажет «готово». Сетевой сбой
// посреди ожидания не валит шаг сразу — линия доставки идёт минуты, и
// единичный провал не её вина; но на исходе срока называется последняя ошибка.
func waitUntil(t *testing.T, step string, limit time.Duration, probe func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last error
	for {
		done, err := probe()
		if err == nil && done {
			return
		}
		last = err
		if time.Now().After(deadline) {
			if last != nil {
				fail(t, step+" (не дождались за "+limit.String()+")", last)
			}
			t.Fatalf("%s: не дождались за %s", step, limit)
		}
		time.Sleep(10 * time.Second)
	}
}

func init() {
	// Токен прогона — из секрета песочницы; вход paas-cli с машины владельца
	// сюда не подставляем: это его боевая организация.
	if os.Getenv("PAAS_API_TOKEN") == "" && os.Getenv("PAAS_SESSION_TOKEN") == "" {
		fmt.Fprintln(os.Stderr, "кластерный прогон идёт только в организации-песочнице: задайте PAAS_API_TOKEN её токеном")
		os.Exit(1)
	}
}
