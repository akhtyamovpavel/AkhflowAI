package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"akhflow.ai/akhflow/internal/store"
)

func TestSendToFakeSMTP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	message := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		fmt.Fprint(conn, "220 localhost ESMTP\r\n")
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "EHLO "):
				fmt.Fprint(conn, "250-localhost\r\n250 OK\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				fmt.Fprint(conn, "250 OK\r\n")
			case line == "DATA":
				fmt.Fprint(conn, "354 Enter message\r\n")
				var body strings.Builder
				for scanner.Scan() && scanner.Text() != "." {
					body.WriteString(scanner.Text())
					body.WriteByte('\n')
				}
				message <- body.String()
				fmt.Fprint(conn, "250 queued\r\n")
			case line == "QUIT":
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprint(conn, "500 unsupported\r\n")
			}
		}
	}()
	config := Config{Address: listener.Addr().String(), From: "sender@example.invalid"}
	item := store.EmailNotification{TaskID: 42, Recipient: "recipient@example.invalid", Title: "Проверка", OldStatus: "todo", NewStatus: "done"}
	if err := Send(context.Background(), config, item); err != nil {
		t.Fatal(err)
	}
	body := <-message
	for _, part := range []string{"recipient@example.invalid", "Проверка", "todo", "done", "Subject:"} {
		if !strings.Contains(body, part) {
			t.Errorf("message does not contain %q", part)
		}
	}
}

func TestConfigRequiresCompleteSettings(t *testing.T) {
	for _, key := range []string{"SMTP_ADDR", "SMTP_FROM", "SMTP_USERNAME", "SMTP_PASSWORD"} {
		t.Setenv(key, "")
	}
	_, enabled, err := FromEnv()
	if err != nil || enabled {
		t.Fatalf("empty config: enabled=%v err=%v", enabled, err)
	}
	t.Setenv("SMTP_ADDR", "localhost:25")
	_, _, err = FromEnv()
	if err == nil {
		t.Fatal("incomplete config was accepted")
	}
	t.Setenv("SMTP_FROM", "sender@example.invalid")
	_, enabled, err = FromEnv()
	if err != nil || !enabled {
		t.Fatalf("valid config: enabled=%v err=%v", enabled, err)
	}
}
