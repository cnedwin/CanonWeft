package hostsignals

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParentCancellationCancelsShutdownContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := NotifyContext(parent)
	defer stop()
	cancel()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatalf("shutdown cause: %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not request shutdown")
	}
}

func TestStopCancelsShutdownContext(t *testing.T) {
	ctx, stop := NotifyContext(context.Background())
	stop()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("releasing signal subscription did not cancel the context")
	}
}

// TestSignalChild is the real HTTP consumer of the production signal context.
// It intentionally has no database, provider or application runtime dependency.
func TestSignalChild(t *testing.T) {
	if os.Getenv("STUDIO_HOST_SIGNALS_CHILD") != "drain" {
		return
	}
	if err := drainChild(); err != nil {
		t.Fatal(err)
	}
}

func drainChild() error {
	ctx, stop := NotifyContext(context.Background())
	defer stop()
	// These are process-lifecycle hooks, matching the deferred stop pattern in
	// Studio. A default signal exit cannot run any of them.
	defer fmt.Println("CLEANUP worker")
	defer fmt.Println("CLEANUP backup")
	defer fmt.Println("CLEANUP log")
	release := make(chan struct{})
	var releaseOnce sync.Once
	inputFailure := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			switch scanner.Text() {
			case "release":
				releaseOnce.Do(func() { close(release) })
			case "interrupt":
				if err := interruptSelf(); err != nil {
					inputFailure <- err
					return
				}
			}
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Println("ENTERED")
		<-release
		_, _ = io.WriteString(w, "drained")
	})}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	fmt.Println("READY " + listener.Addr().String())
	select {
	case <-ctx.Done():
	case err := <-inputFailure:
		return err
	case err := <-served:
		return fmt.Errorf("server stopped before shutdown request: %w", err)
	}
	fmt.Println("STOPPING")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	fmt.Println("DRAINED")
	return nil
}

type lockedOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

// The regression catches a missing signal subscription through the first real
// consumer-visible effect: an in-flight HTTP request drains before process exit.
func assertSignalDrainsHTTP(t *testing.T, configure func(*exec.Cmd), send func(*os.Process, io.Writer) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalChild$", "-test.v")
	command.Env = append(os.Environ(), "STUDIO_HOST_SIGNALS_CHILD=drain")
	if configure != nil {
		configure(command)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var output lockedOutput
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	lines := make(chan string, 32)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = output.Write([]byte(line + "\n"))
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	await := func(prefix string) string {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("child exited before %s; cleanup did not complete:\n%s", prefix, output.String())
				}
				if strings.HasPrefix(line, prefix) {
					return strings.TrimPrefix(line, prefix)
				}
			case <-ctx.Done():
				t.Fatalf("child did not reach %s: %v\n%s", prefix, ctx.Err(), output.String())
			}
		}
	}
	address := await("READY ")
	response := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/hold", nil)
		if err != nil {
			response <- err
			return
		}
		client := &http.Client{Transport: &http.Transport{Proxy: nil}}
		defer client.CloseIdleConnections()
		r, err := client.Do(request)
		if err != nil {
			response <- err
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err == nil && (r.StatusCode != http.StatusOK || string(body) != "drained") {
			err = fmt.Errorf("in-flight HTTP response: status=%d body=%q", r.StatusCode, body)
		}
		response <- err
	}()
	await("ENTERED")
	if err := send(command.Process, stdin); err != nil {
		t.Fatal(err)
	}
	await("STOPPING")
	// The handler is still blocked on explicit release. Shutdown must not have
	// returned merely because the listener has closed.
	select {
	case err := <-response:
		t.Fatalf("in-flight request ended before release: %v", err)
	default:
	}
	if _, err := io.WriteString(stdin, "release\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-response:
		if err != nil {
			t.Fatalf("in-flight request was not drained: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	await("DRAINED")
	<-scanned
	if err := command.Wait(); err != nil {
		t.Fatalf("child did not return normally: %v\n%s", err, output.String())
	}
	for _, marker := range []string{"CLEANUP log\n", "CLEANUP backup\n", "CLEANUP worker\n"} {
		if count := strings.Count(output.String(), marker); count != 1 {
			t.Fatalf("%q ran %d times:\n%s", marker, count, output.String())
		}
	}
	t.Log("in-flight HTTP response drained; log, backup and worker cleanup each ran once")
}
