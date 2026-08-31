package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

var platform *Client

func TestMain(m *testing.M) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "сквозные проверки не запущены: %v\n", err)
		os.Exit(1)
	}
	platform = New(cfg)
	fmt.Fprintf(os.Stderr, "проверяем платформу %s\n", cfg.APIURL)
	os.Exit(m.Run())
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), platform.Config().Timeout)
	t.Cleanup(cancel)
	return ctx
}

// fail называет причину падения так, чтобы из отчёта было видно виновника:
// «не ответила» — авария доступности, «отказала» — платформа жива, но не
// пустила или сломалась внутри.
func fail(t *testing.T, step string, err error) {
	t.Helper()
	var unreachable *UnreachableError
	var refused *RefusedError
	switch {
	case errors.As(err, &unreachable):
		t.Fatalf("%s: НЕ ОТВЕТИЛА — %v", step, err)
	case errors.As(err, &refused):
		t.Fatalf("%s: ОТКАЗ — %v", step, err)
	default:
		t.Fatalf("%s: %v", step, err)
	}
}

func age(ts time.Time) string {
	if ts.IsZero() {
		return "никогда"
	}
	return time.Since(ts).Truncate(time.Second).String() + " назад"
}
