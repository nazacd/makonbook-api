package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

// fakeSMTP is a mail server that speaks just enough SMTP, with STARTTLS
// (starttls false: without it) and AUTH PLAIN, and keeps what it got.
func fakeSMTP(t *testing.T, starttls bool) (port int, cfg *tls.Config, got chan string) {
	t.Helper()
	certSrv := httptest.NewUnstartedServer(nil)
	certSrv.StartTLS()
	t.Cleanup(certSrv.Close)
	serverTLS := &tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12}
	pool := x509.NewCertPool()
	pool.AddCert(certSrv.Certificate())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r, w := bufio.NewReader(conn), conn
		say := func(s string) { _, _ = w.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var log []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			cmd := strings.ToUpper(strings.Fields(line + " x")[0])
			switch cmd {
			case "EHLO":
				if starttls {
					say("250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN")
				} else {
					say("250 fake")
				}
			case "STARTTLS":
				say("220 go ahead")
				tc := tls.Server(conn, serverTLS)
				if err := tc.Handshake(); err != nil {
					return
				}
				r, w = bufio.NewReader(tc), tc
				log = append(log, "TLS")
			case "AUTH":
				log = append(log, line)
				say("235 ok")
			case "MAIL", "RCPT":
				log = append(log, line)
				say("250 ok")
			case "DATA":
				say("354 go")
				var body strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					body.WriteString(l)
				}
				log = append(log, body.String())
				say("250 queued")
			case "QUIT":
				say("221 bye")
				got <- strings.Join(log, "\n")
				return
			default:
				say("502 no")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}, got
}

func TestSMTP(t *testing.T) {
	port, cfg, got := fakeSMTP(t, true)
	s := &SMTP{Host: "127.0.0.1", Port: port, Username: "no-reply@makonbook.uz", Password: "app-password",
		From: "SATMAKON <no-reply@makonbook.uz>", TLS: cfg}
	if err := s.Send(context.Background(), "malika@example.com", "Your SATMAKON code: 123456", "Your code is 123456.\nIt expires in 10 minutes."); err != nil {
		t.Fatal(err)
	}
	log := <-got
	for _, want := range []string{"TLS\nAUTH PLAIN", "MAIL FROM:<no-reply@makonbook.uz>", "RCPT TO:<malika@example.com>",
		"From: \"SATMAKON\" <no-reply@makonbook.uz>\r\n", "To: malika@example.com\r\n", "Subject: Your SATMAKON code: 123456\r\n",
		"Content-Type: text/plain; charset=utf-8", "\r\n\r\nYour code is 123456.\r\nIt expires in 10 minutes.\r\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
}

func TestSMTPNeedsTLS(t *testing.T) {
	port, cfg, _ := fakeSMTP(t, false)
	s := &SMTP{Host: "127.0.0.1", Port: port, From: "no-reply@makonbook.uz", TLS: cfg}
	if err := s.Send(context.Background(), "a@example.com", "x", "y"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("err = %v", err)
	}
	if _, err := message(&mail.Address{Address: "no-reply@makonbook.uz"}, "a@example.com\r\nBcc: x@y.z", "x", "y"); err == nil {
		t.Error("header injection accepted")
	}
}
