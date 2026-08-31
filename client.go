// Package e2e — сквозные проверки живой платформы: ходим её же публичным API,
// как это делает пользователь с машины.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultAPIURL  = "https://api.paas.traumtech.ru"
	defaultWebURL  = "https://paas.traumtech.ru"
	defaultAuthURL = "https://auth.paas.traumtech.ru"

	defaultTimeout = 20 * time.Second
)

// Config — куда ходим и чем предъявляемся. Адреса по умолчанию прод; доступ
// обязателен: без него проверка не «пропускается», а падает, иначе сломанная
// платформа выглядела бы зелёной. По расписанию ходим машинным токеном; на
// своей машине хватает сохранённого входа paas-cli — отдельный токен ради
// разового прогона заводить не нужно.
type Config struct {
	APIURL       string
	WebURL       string
	AuthURL      string
	Token        string
	SessionToken string
	Timeout      time.Duration
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		APIURL:       envOr("PAAS_API_URL", defaultAPIURL),
		WebURL:       envOr("PAAS_WEB_URL", defaultWebURL),
		AuthURL:      envOr("PAAS_AUTH_URL", defaultAuthURL),
		Token:        os.Getenv("PAAS_API_TOKEN"),
		SessionToken: os.Getenv("PAAS_SESSION_TOKEN"),
		Timeout:      defaultTimeout,
	}
	if cfg.Token == "" && cfg.SessionToken == "" {
		return Config{}, errors.New("не задан ни PAAS_API_TOKEN (машинный или личный токен), ни PAAS_SESSION_TOKEN (сохранённый вход paas-cli)")
	}
	if raw := os.Getenv("PAAS_E2E_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("PAAS_E2E_TIMEOUT=%q: %w", raw, err)
		}
		cfg.Timeout = d
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimRight(v, "/")
	}
	return fallback
}

// UnreachableError — платформа не ответила (сеть, TLS, таймаут). Отличать от
// отказа обязательно: «недоступна» и «работает, но не пустила» — разные аварии
// с разным виновником.
type UnreachableError struct {
	Method string
	URL    string
	Err    error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("платформа не ответила на %s %s: %v", e.Method, e.URL, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// RefusedError — платформа ответила, но отказом.
type RefusedError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *RefusedError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("платформа отказала на %s %s: %d %s", e.Method, e.URL, e.Status, body)
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (c *Client) Config() Config { return c.cfg }

// Get читает ресурс API от имени владельца токена.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, c.cfg.APIURL+path, true, nil, out)
}

// Post отправляет тело в API от имени владельца токена. Используется только
// для неизменяющих ручек-отчётов (сверка совместимости).
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, c.cfg.APIURL+path, true, body, out)
}

// StatusOf возвращает код ответа, не считая отказ ошибкой: нужно там, где
// проверяется сам отказ (запрос без токена) или доступность внешнего входа.
func (c *Client) StatusOf(ctx context.Context, rawURL string, auth bool) (int, error) {
	req, err := c.request(ctx, http.MethodGet, rawURL, auth, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, &UnreachableError{Method: http.MethodGet, URL: rawURL, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (c *Client) do(ctx context.Context, method, rawURL string, auth bool, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("сериализация тела %s %s: %w", method, rawURL, err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := c.request(ctx, method, rawURL, auth, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &UnreachableError{Method: method, URL: rawURL, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &UnreachableError{Method: method, URL: rawURL, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &RefusedError{Method: method, URL: rawURL, Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ответ %s %s не разбирается: %w", method, rawURL, err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, rawURL string, auth bool, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("запрос %s %s: %w", method, rawURL, err)
	}
	req.Header.Set("Accept", "application/json")
	switch {
	case !auth:
	case c.cfg.Token != "":
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	default:
		req.Header.Set("X-Session-Token", c.cfg.SessionToken)
	}
	return req, nil
}
