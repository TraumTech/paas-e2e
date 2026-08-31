#!/usr/bin/env python3
"""Превращает объявленные платформой требуемые права в роль кластера.

Читает ответ платформы со stdin, печатает манифесты в stdout. Роль выдаётся
пользователю `paas-platform` — под этим именем платформа предъявляется
API-серверу эфемерного кластера.
"""

import json
import sys

USER = "paas-platform"
ROLE = "paas-platform"


def main() -> int:
    payload = json.load(sys.stdin)
    rules = payload.get("rules") or []
    if not rules:
        print("платформа не объявила требуемых прав", file=sys.stderr)
        return 1

    role = {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "ClusterRole",
        "metadata": {"name": ROLE},
        "rules": [
            {
                # Пустая группа в объявлении — основная группа API.
                "apiGroups": rule["api_groups"] or [""],
                "resources": rule["resources"],
                "verbs": rule["verbs"],
            }
            for rule in rules
        ],
    }
    binding = {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "ClusterRoleBinding",
        "metadata": {"name": ROLE},
        "roleRef": {
            "apiGroup": "rbac.authorization.k8s.io",
            "kind": "ClusterRole",
            "name": ROLE,
        },
        "subjects": [
            {"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": USER}
        ],
    }
    json.dump({"apiVersion": "v1", "kind": "List", "items": [role, binding]}, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
