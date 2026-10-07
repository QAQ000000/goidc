package notification

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSMTPRequiresSTARTTLSAndRedactsErrors(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "220 test\r\n")
		reader := bufio.NewReader(conn)
		reader.ReadString('\n')
		fmt.Fprint(conn, "250 test\r\n")
		io.Copy(io.Discard, conn)
	}()
	c := SMTPConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "sender@example.test"}
	if e = c.Send(store.EmailJob{Recipient: "to@example.test", Subject: "test", Body: "secret"}); e == nil || e.Error() != "SMTP delivery failed" {
		t.Fatalf("unsafe result: %v", e)
	}
	<-done
}

func TestSMTPPlaintextMessageAndUTF8Headers(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	message := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			message <- ""
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "220 test\r\n")
		r := bufio.NewReader(conn)
		var body strings.Builder
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			if strings.HasPrefix(line, "DATA") {
				fmt.Fprint(conn, "354 data\r\n")
				for {
					line, e = r.ReadString('\n')
					if e != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(line)
				}
				fmt.Fprint(conn, "250 ok\r\n")
			} else if strings.HasPrefix(line, "QUIT") {
				fmt.Fprint(conn, "221 bye\r\n")
				message <- body.String()
				return
			} else {
				fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	c := SMTPConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "sender@example.test", FromName: "邮件服务", TLSMode: "none"}
	if e = c.Send(store.EmailJob{Recipient: "to@example.test", Subject: "测试邮件", Body: "message"}); e != nil {
		t.Fatal(e)
	}
	raw := <-message
	if !strings.Contains(raw, "From: =?utf-8?") || !strings.Contains(raw, "Subject: =?UTF-8?") {
		t.Fatal("UTF8 headers were not encoded")
	}
	c.Username = "secret-user"
	c.Password = "secret-password"
	if e = c.Send(store.EmailJob{Recipient: "to@example.test"}); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("plaintext authentication accepted or leaked")
	}
}
