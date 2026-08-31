package e2e

import (
	"net/url"
	"testing"
)

// Кластер, с которым платформа потеряла связь, ломает выкатки молча: ручки
// отвечают, а катить некуда.
func TestClustersConnected(t *testing.T) {
	for _, cluster := range listClusters(t) {
		t.Run(cluster.Name, func(t *testing.T) {
			if cluster.Endpoint == "" {
				t.Errorf("у кластера нет адреса: %+v", cluster)
			}
			if !cluster.Connected {
				t.Fatalf("платформа не имеет связи с кластером (окружение %s, последний контакт %s)",
					cluster.Environment, age(cluster.LastContactAt))
			}
			t.Logf("окружение %s, последний контакт %s", cluster.Environment, age(cluster.LastContactAt))
		})
	}
}

// Состояние выкаченного сервиса глазами платформы: если процесс выкачен, но
// готовых реплик нет — прод лежит, даже когда API платформы бодр.
func TestDeployedWorkloadsHealthy(t *testing.T) {
	services := listServices(t)
	deployed := 0

	for _, environment := range environments(t) {
		for _, service := range services {
			var status WorkloadStatus
			path := "/organizations/current/environments/" + url.PathEscape(environment) +
				"/services/" + service.ID + "/status"
			if err := platform.Get(testContext(t), path, &status); err != nil {
				t.Run(environment+"/"+service.Name, func(t *testing.T) {
					fail(t, "состояние сервиса "+service.Name+" в окружении "+environment, err)
				})
				continue
			}
			if !status.Deployed {
				continue
			}
			deployed++

			t.Run(environment+"/"+service.Name, func(t *testing.T) {
				for _, process := range status.Processes {
					if process.ReadyReplicas < 1 {
						t.Errorf("процесс %s: готовых реплик %d из %d желаемых",
							process.Name, process.ReadyReplicas, process.DesiredReplicas)
					}
					for _, pod := range process.Pods {
						if !pod.Ready {
							t.Errorf("процесс %s, экземпляр %s: не готов (%s, %s %s)",
								process.Name, pod.Name, pod.Phase, pod.Reason, pod.Message)
						}
					}
				}
			})
		}
	}

	if deployed == 0 {
		t.Fatal("ни один сервис не значится выкаченным ни в одном окружении")
	}
}

// Домены, которые платформа сгенерировала выкаткой, должны отвечать снаружи:
// так проверяются DNS, сертификат и маршрут до процесса. Любой ответ годится —
// мы проверяем достижимость входа, а не поведение чужого сервиса.
func TestDeclaredHostsReachable(t *testing.T) {
	var routes RouteList
	if err := platform.Get(testContext(t), "/organizations/current/service-routes", &routes); err != nil {
		fail(t, "маршруты сервисов", err)
	}

	seen := map[string]bool{}
	for _, route := range routes.Declared {
		if route.Host == "" || seen[route.Host] {
			continue
		}
		seen[route.Host] = true

		t.Run(route.Host, func(t *testing.T) {
			status, err := platform.StatusOf(testContext(t), "https://"+route.Host+"/", false)
			if err != nil {
				fail(t, "хост "+route.Host, err)
			}
			t.Logf("сервис %s (%s), ответ %d", route.ServiceName, route.Environment, status)
		})
	}

	if len(seen) == 0 {
		t.Skip("ни одного объявленного хоста")
	}
}

func listClusters(t *testing.T) []Cluster {
	t.Helper()
	var list ClusterList
	if err := platform.Get(testContext(t), "/organizations/current/clusters", &list); err != nil {
		fail(t, "перечень кластеров", err)
	}
	if len(list.Clusters) == 0 {
		t.Fatal("к организации не подключено ни одного кластера")
	}
	return list.Clusters
}

// Проверяем те окружения, в которых у организации есть кластер: остальные
// пусты по определению.
func environments(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var result []string
	for _, cluster := range listClusters(t) {
		if cluster.Environment == "" || seen[cluster.Environment] {
			continue
		}
		seen[cluster.Environment] = true
		result = append(result, cluster.Environment)
	}
	return result
}
