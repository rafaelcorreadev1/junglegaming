//go:build integration

// Package recovery runs the real binary as separate processes and kills them at the worst moments:
// between commit and ack, between publishing and marking, and in the middle of a load.
// Each test gets its own schema and queues, so it never disturbs the Compose instances.
package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/infra/faults"
	"wagering-service/internal/testdb"
	"wagering-service/internal/testenv"
	"wagering-service/test/e2e"
)

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
)

func build(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wagering-recovery")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(dir, "app")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		output, err := exec.Command("go", "build", "-o", binary, "wagering-service/cmd/app").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, output)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binary
}

type stack struct {
	pool   *pgxpool.Pool
	queue  *e2e.Queue
	events string
	env    []string
}

func newStack(t *testing.T) *stack {
	t.Helper()
	build(t)
	pool := testdb.NewPool(t)
	queue := e2e.NewQueue(t)
	newQueue := func(name string) string {
		created, err := queue.Client.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
			QueueName:  aws.String(fmt.Sprintf("recovery-%s-%d.fifo", name, time.Now().UnixNano())),
			Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
		})
		if err != nil {
			t.Fatalf("create queue: %v", err)
		}
		t.Cleanup(func() {
			_, _ = queue.Client.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
		})
		return *created.QueueUrl
	}
	queue.URL = newQueue("wagers")
	s := &stack{pool: pool, queue: queue, events: newQueue("events")}

	keycloak := testenv.Lookup(t, "TEST_KEYCLOAK_URL", "http://localhost:8081")
	s.env = []string{
		"DATABASE_URL=" + testenv.DatabaseURL(t) + "&search_path=" + pool.Config().ConnConfig.RuntimeParams["search_path"],
		"LOG_LEVEL=info",
		"SHUTDOWN_TIMEOUT=5s",
		// Keycloak signs tokens with the host-facing issuer whatever address the keys are fetched from.
		"AUTH_ISSUER=" + testenv.Lookup(t, "TEST_AUTH_ISSUER", "http://localhost:8081/realms/wagering"),
		"AUTH_JWKS_URL=" + keycloak + "/realms/wagering/protocol/openid-connect/certs",
		"AWS_REGION=us-east-1",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"SQS_ENDPOINT=" + testenv.SQSEndpoint(t),
		"OUTBOX_QUEUE_URL=" + s.events,
		"WAGER_QUEUE_URL=" + queue.URL,
		"WAGER_DLQ_URL=" + newQueue("dlq"),
		"WAGER_QUEUE_PROVIDERS=provider-a,provider-b",
		"WAGER_VISIBILITY_TIMEOUT=3s",
		"OUTBOX_POLL_INTERVAL=200ms",
		"OUTBOX_LEASE=3s",
		"REFERENCE_POLL_INTERVAL=200ms",
		"REFERENCE_RETRY_BASE_DELAY=200ms",
		"REFERENCE_RETRY_MAX_DELAY=1s",
		"REFERENCE_MAX_ATTEMPTS=5",
		"REFERENCE_LEASE=3s",
	}
	return s
}

type process struct {
	url    string
	cmd    *exec.Cmd
	logs   *lockedBuffer
	exited chan struct{}
	err    error
}

func (s *stack) start(t *testing.T, extra ...string) *process {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	p := &process{url: "http://" + addr, logs: &lockedBuffer{}, exited: make(chan struct{})}
	p.cmd = exec.Command(binary)
	// exec keeps the last value of a repeated variable, so extra wins over the defaults.
	p.cmd.Env = append(append(append(os.Environ(), s.env...), "HTTP_ADDR="+addr), extra...)
	p.cmd.Stdout, p.cmd.Stderr = p.logs, p.logs
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		p.kill()
		if t.Failed() {
			t.Logf("logs of %s:\n%s", p.url, p.logs)
		}
	})
	return p
}

func (p *process) kill() {
	select {
	case <-p.exited:
	default:
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
}

func (p *process) waitExit(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.exited:
	case <-time.After(timeout):
		t.Fatalf("%s still running after %v", p.url, timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(p.err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 0
}

func (p *process) waitFault(t *testing.T) {
	t.Helper()
	if code := p.waitExit(t, 60*time.Second); code != faults.ExitCode {
		t.Fatalf("exit code = %d, want the injected %d\n%s", code, faults.ExitCode, p.logs)
	}
	if !strings.Contains(p.logs.String(), "fault injection") {
		t.Fatalf("the process did not exit at the injected point:\n%s", p.logs)
	}
}

func (s *stack) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
