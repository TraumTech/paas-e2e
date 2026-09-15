//go:build cluster

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Kube — минимальный доступ к API кластера прогона. Клиентская библиотека
// Kubernetes сюда не тянется по той же причине, что и в самой платформе: нужны
// несколько чтений, а библиотека размером с половину проекта.
type Kube struct {
	server string
	token  string
	http   *http.Client
}

// KubeConfigFromEnv собирает доступ из того же, что получает машина кластера:
// адрес, закреплённый корневой сертификат и токен. Совпадение этих трёх вещей
// со вчерашними — и есть проверяемый инвариант: кластер новый, доступ прежний.
func KubeConfigFromEnv(token string) (*Kube, error) {
	host := clusterHost()
	encodedCA := os.Getenv("E2E_CA_CRT")
	if encodedCA == "" {
		return nil, errors.New("не задан E2E_CA_CRT")
	}
	if token == "" {
		return nil, errors.New("не задан токен доступа к кластеру")
	}
	ca, err := base64.StdEncoding.DecodeString(encodedCA)
	if err != nil {
		return nil, fmt.Errorf("E2E_CA_CRT не разбирается как base64: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("E2E_CA_CRT не содержит сертификата")
	}

	return &Kube{
		server: "https://" + host + ":6443",
		token:  token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		},
	}, nil
}

// Get читает ресурс кластера. Отказ кластера возвращается как RefusedError —
// у проверок один язык для платформы и для кластера.
func (k *Kube) Get(ctx context.Context, path string, out any) error {
	status, raw, err := k.get(ctx, path)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &RefusedError{Method: http.MethodGet, URL: k.server + path, Status: status, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ответ кластера %s не разбирается: %w", path, err)
	}
	return nil
}

// StatusOf возвращает код ответа, не считая отказ ошибкой: нужно там, где
// проверяется сама граница прав.
func (k *Kube) StatusOf(ctx context.Context, path string) (int, error) {
	status, _, err := k.get(ctx, path)
	return status, err
}

func (k *Kube) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.server+path, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("запрос к кластеру %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Accept", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return 0, nil, &UnreachableError{Method: http.MethodGet, URL: k.server + path, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, &UnreachableError{Method: http.MethodGet, URL: k.server + path, Err: err}
	}
	return resp.StatusCode, raw, nil
}

// Условия объектов Kubernetes читаются одинаково у Deployment и у ClusterIssuer.
type conditioned struct {
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
			Msg    string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func (c conditioned) condition(name string) (status, reason, message string, found bool) {
	for _, cond := range c.Status.Conditions {
		if cond.Type == name {
			return cond.Status, cond.Reason, cond.Msg, true
		}
	}
	return "", "", "", false
}

// clusterHost — имя кластера прогона: под ним подъём кластера прописывает
// адрес, и по нему платформа знает кластер.
func clusterHost() string { return envOr("E2E_CLUSTER_HOST", "e2e.traumtech.ru") }
