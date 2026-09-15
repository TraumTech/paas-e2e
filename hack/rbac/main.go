// Команда rbac превращает объявленные платформой требуемые права в роль
// кластера прогона: спрашивает платформу от лица песочницы и печатает
// манифесты, которые cluster-up.sh кладёт в cloud-init машины.
//
// Роль выдаётся пользователю paas-platform — под этим именем платформа
// предъявляется API-серверу эфемерного кластера (token-auth-file).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	e2e "github.com/TraumTech/paas-e2e"
)

const subject = "paas-platform"

type requiredAccess struct {
	Rules []struct {
		APIGroups []string `json:"api_groups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	} `json:"rules"`
}

type rule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := e2e.ConfigFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var declared requiredAccess
	if err := e2e.New(cfg).Get(ctx, "/organizations/current/clusters/required-access", &declared); err != nil {
		return fmt.Errorf("требуемые платформой права: %w", err)
	}
	if len(declared.Rules) == 0 {
		return fmt.Errorf("платформа не объявила требуемых прав")
	}

	rules := make([]rule, 0, len(declared.Rules))
	for _, r := range declared.Rules {
		groups := r.APIGroups
		if len(groups) == 0 {
			// Пустая группа в объявлении — основная группа API.
			groups = []string{""}
		}
		rules = append(rules, rule{APIGroups: groups, Resources: r.Resources, Verbs: r.Verbs})
	}

	meta := map[string]string{"name": subject}
	role := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   meta,
		"rules":      rules,
	}
	binding := map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   meta,
		"roleRef": map[string]string{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     subject,
		},
		"subjects": []map[string]string{
			{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": subject},
		},
	}
	list := map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{role, binding}}
	return json.NewEncoder(os.Stdout).Encode(list)
}
