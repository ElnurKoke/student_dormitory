//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"student_dormitory/internal/platform/database"
)

func TestServerProcessCleanRestartAndSIGTERM(t *testing.T) {
	if os.Getenv("DORM_SERVER_TEST_HELPER") == "1" {
		if err := run(slog.New(slog.NewJSONHandler(os.Stdout, nil))); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dormitory.db")
	for iteration := 0; iteration < 2; iteration++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("local TCP is required for the process acceptance test", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		address := "127.0.0.1:" + strconv.Itoa(port)
		command := exec.Command(os.Args[0], "-test.run=^TestServerProcessCleanRestartAndSIGTERM$")
		command.Env = append(os.Environ(), "DORM_SERVER_TEST_HELPER=1", "APP_ENV=production", "DEV_LOGIN_ENABLED=false", "HTTP_ADDR="+address, "DATABASE_PATH="+dbPath, "FILES_DIR="+filepath.Join(dir, "files"), "SHUTDOWN_TIMEOUT=2s")
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err = command.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- command.Wait() }()
		client := &http.Client{Timeout: time.Second}
		ready := false
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			response, err := client.Get("http://" + address + "/healthz")
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				ready = response.StatusCode == 200
				if ready {
					break
				}
			}
			select {
			case err := <-exited:
				t.Fatalf("server exited before ready: %v %s", err, output.String())
			case <-time.After(25 * time.Millisecond):
			}
		}
		if !ready {
			command.Process.Kill()
			<-exited
			t.Fatal("server did not become ready")
		}
		response, err := client.Get("http://" + address + "/catalog")
		if err != nil {
			command.Process.Kill()
			<-exited
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			command.Process.Kill()
			<-exited
			t.Fatal("catalogue unavailable after startup")
		}
		if iteration == 0 && !strings.Contains(string(body), "Общежития не найдены") {
			t.Error("clean production startup seeded data")
		}
		if iteration == 1 && !strings.Contains(string(body), "Persistent process fixture") {
			t.Error("restart lost committed registry data")
		}
		if err = command.Process.Signal(syscall.SIGTERM); err != nil {
			command.Process.Kill()
			<-exited
			t.Fatal(err)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Fatal("SIGTERM returned failure", err, output.String())
			}
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			<-exited
			t.Fatal("graceful shutdown timed out")
		}
		client.CloseIdleConnections()
		if !strings.Contains(output.String(), "server stopped gracefully") {
			t.Fatal("graceful shutdown not confirmed")
		}
		db, err := database.Open(context.Background(), dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err = database.CheckIntegrity(context.Background(), db); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if iteration == 0 {
			_, err = db.ExecContext(context.Background(), `INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts,published) VALUES('process-dorm','Persistent process fixture','address','operator','basis','contacts',1)`)
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
		db.Close()
	}
}
