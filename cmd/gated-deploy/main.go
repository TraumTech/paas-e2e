// gated-deploy — выкатка сборки сервиса платформы из его CI под гейтом
// сквозных проверок (TRA-516): `deploy` выкатывает сборку и ждёт готовности,
// запомнив версию, которая работала до неё; `rollback` возвращает ту версию.
// Сами проверки гоняет workflow между этими шагами.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	e2e "github.com/TraumTech/paas-e2e"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "использование: gated-deploy deploy|rollback [флаги]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	service := flags.String("service", "", "идентификатор сервиса")
	environment := flags.String("environment", "prod", "окружение")
	build := flags.String("build", "", "сборка (deploy)")
	revision := flags.String("revision", "", "ревизия коммита сборки (deploy)")
	version := flags.String("version", "", "версия, на которую откатываемся (rollback)")
	timeout := flags.Duration("timeout", 10*time.Minute, "сколько ждать готовности")
	_ = flags.Parse(os.Args[2:])

	cfg, err := e2e.ConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	d := deployer{platform: e2e.New(cfg), service: *service, environment: *environment, timeout: *timeout}
	if d.service == "" {
		fatal(errors.New("не задан -service"))
	}

	switch os.Args[1] {
	case "deploy":
		if *build == "" || *revision == "" {
			fatal(errors.New("deploy требует -build и -revision"))
		}
		previous, err := d.runningVersion()
		if err != nil {
			fatal(fmt.Errorf("версия до выкатки: %w", err))
		}
		// Строка для $GITHUB_OUTPUT: на неё откатывается гейт.
		fmt.Printf("previous=%s\n", previous)
		if err := d.deploy(map[string]string{"build_id": *build}); err != nil {
			fatal(err)
		}
		if err := d.waitReady(*revision); err != nil {
			fatal(err)
		}
	case "rollback":
		if *version == "" {
			fatal(errors.New("rollback требует -version"))
		}
		if err := d.deploy(map[string]string{"version_id": *version}); err != nil {
			fatal(err)
		}
		if err := d.waitReady(""); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("неизвестная команда %q", os.Args[1]))
	}
}

type deployer struct {
	platform    *e2e.Client
	service     string
	environment string
	timeout     time.Duration
}

func (d deployer) status(ctx context.Context) (e2e.WorkloadStatus, error) {
	var status e2e.WorkloadStatus
	path := "/organizations/current/environments/" + url.PathEscape(d.environment) + "/services/" + d.service + "/status"
	err := d.platform.Get(ctx, path, &status)
	return status, err
}

// runningVersion — версия окружения, чей образ сейчас работает: тег образа
// сборки — ревизия коммита. Пусто, если сервис ещё не выкачен.
func (d deployer) runningVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	status, err := d.status(ctx)
	if err != nil {
		return "", err
	}
	if !status.Deployed || len(status.Processes) == 0 {
		return "", nil
	}
	image := status.Processes[0].Image
	revision := image[strings.LastIndex(image, ":")+1:]

	var versions []e2e.Version
	if err := d.platform.Get(ctx, "/services/"+d.service+"/versions", &versions); err != nil {
		return "", err
	}
	var found *e2e.Version
	for i, v := range versions {
		if v.Environment == d.environment && v.CommitRevision == revision && (found == nil || v.Number > found.Number) {
			found = &versions[i]
		}
	}
	if found == nil {
		return "", fmt.Errorf("в окружении %s нет версии ревизии %s (образ %s)", d.environment, revision, image)
	}
	fmt.Fprintf(os.Stderr, "работает версия %d (%s)\n", found.Number, revision)
	return found.ID, nil
}

func (d deployer) deploy(body map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	body["service_id"] = d.service
	path := "/organizations/current/environments/" + url.PathEscape(d.environment) + "/deployments"
	if err := d.platform.Post(ctx, path, body, nil); err != nil {
		return fmt.Errorf("выкатка: %w", err)
	}
	return nil
}

// waitReady ждёт, пока у каждого процесса работают только готовые экземпляры
// нужного образа (revision пусто — любого) в желаемом числе.
func (d deployer) waitReady(revision string) error {
	deadline := time.Now().Add(d.timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		status, err := d.status(ctx)
		cancel()
		state := "нет ответа"
		if err == nil {
			ready, why := isReady(status, revision)
			if ready {
				fmt.Fprintln(os.Stderr, "выкатка готова")
				return nil
			}
			state = why
		} else {
			state = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("выкатка не готова за %s: %s", d.timeout, state)
		}
		fmt.Fprintf(os.Stderr, "ждём: %s\n", state)
		time.Sleep(10 * time.Second)
	}
}

func isReady(status e2e.WorkloadStatus, revision string) (bool, string) {
	if !status.Deployed || len(status.Processes) == 0 {
		return false, "сервис не выкачен"
	}
	for _, p := range status.Processes {
		if revision != "" && !strings.HasSuffix(p.Image, ":"+revision) {
			return false, fmt.Sprintf("процесс %s ещё на образе %s", p.Name, p.Image)
		}
		running := 0
		for _, pod := range p.Pods {
			if pod.Phase == "Succeeded" || pod.Phase == "Failed" {
				continue
			}
			running++
			if !pod.Ready {
				return false, fmt.Sprintf("процесс %s: экземпляр %s не готов %s", p.Name, pod.Name, pod.Reason)
			}
		}
		if p.DesiredReplicas < 1 || p.ReadyReplicas < p.DesiredReplicas || int64(running) != p.DesiredReplicas {
			return false, fmt.Sprintf("процесс %s: готово %d из %d, экземпляров %d", p.Name, p.ReadyReplicas, p.DesiredReplicas, running)
		}
	}
	return true, ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gated-deploy:", err)
	os.Exit(1)
}
