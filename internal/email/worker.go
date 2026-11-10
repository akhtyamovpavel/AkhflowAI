package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/store"
)

type Config struct {
	Address  string
	From     string
	Username string
	Password string
}

func FromEnv() (Config, bool, error) {
	c := Config{
		Address: os.Getenv("SMTP_ADDR"), From: os.Getenv("SMTP_FROM"),
		Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"),
	}
	if c.Address == "" && c.From == "" && c.Username == "" && c.Password == "" {
		return c, false, nil
	}
	if _, _, err := net.SplitHostPort(c.Address); err != nil {
		return c, false, fmt.Errorf("SMTP_ADDR must contain host and port")
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil || from.Address != c.From || strings.ContainsAny(c.From, "\r\n") {
		return c, false, fmt.Errorf("SMTP_FROM must be an email address")
	}
	if (c.Username == "") != (c.Password == "") {
		return c, false, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must be set together")
	}
	return c, true, nil
}

func Send(ctx context.Context, config Config, item store.EmailNotification) error {
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil {
		return err
	}
	to, err := mail.ParseAddress(item.Recipient)
	if err != nil || to.Address != item.Recipient || strings.ContainsAny(item.Recipient, "\r\n") {
		return fmt.Errorf("invalid recipient")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", config.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("SMTP server must support STARTTLS")
		}
	}
	if config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(config.From); err != nil {
		return err
	}
	if err := client.Rcpt(item.Recipient); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	subject := mime.QEncoding.Encode("utf-8", fmt.Sprintf("Изменился статус задачи #%d", item.TaskID))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nЗадача #%d: %s\r\nСтатус: %s → %s\r\n", config.From, item.Recipient, subject, item.TaskID, item.Title, item.OldStatus, item.NewStatus)
	if _, err := w.Write([]byte(message)); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// DATA was accepted; a failed QUIT must not cause an otherwise delivered email to be retried.
	_ = client.Quit()
	return nil
}

func Run(ctx context.Context, db *store.Store, config Config) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		for {
			item, err := db.ClaimEmailNotification(ctx)
			if err != nil {
				log.Printf("email queue: %v", err)
				break
			}
			if item == nil {
				break
			}
			if err := Send(ctx, config, *item); err != nil {
				log.Printf("email delivery failed for notification %d: %v", item.ID, err)
				continue
			}
			if err := db.DeleteEmailNotification(ctx, item.ID); err != nil {
				log.Printf("email queue acknowledgement %d: %v", item.ID, err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
