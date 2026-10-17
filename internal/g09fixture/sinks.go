// Package g09fixture provides opt-in acceptance infrastructure, never product routes.
package g09fixture

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxMessage = 1 << 20

// Acceptance records protocol acceptance, not recipient delivery. It deliberately
// excludes plaintext addresses, credentials and message bodies.
type Acceptance struct {
	Sequence      uint64    `json:"sequence"`
	Channel       string    `json:"channel"`
	AcceptedAt    time.Time `json:"acceptedAt"`
	RecipientHash string    `json:"recipientHash"`
	MessageHash   string    `json:"messageHash"`
}

// Journal serializes and fsyncs every record before the protocol returns success.
// Its directory must already exist, be owned by the caller and have mode 0700.
type Journal struct {
	mu       sync.Mutex
	file     *os.File
	sequence uint64
	records  []Acceptance
}

func NewJournal(path string) (*Journal, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("journal requires absolute private path")
	}
	if err := privateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &Journal{file: f}, nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (j *Journal) accept(channel, recipient, message string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := Acceptance{Sequence: j.sequence + 1, Channel: channel, AcceptedAt: time.Now().UTC(), RecipientHash: digest(recipient), MessageHash: digest(message)}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = j.file.Write(b); err != nil {
		return err
	}
	if err = j.file.Sync(); err != nil {
		return err
	}
	j.sequence = r.Sequence
	j.records = append(j.records, r)
	return nil
}
func (j *Journal) Snapshot() []Acceptance {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Acceptance{}, j.records...)
}
func (j *Journal) Close() error { j.mu.Lock(); defer j.mu.Unlock(); return j.file.Close() }

// SMSHandler implements generic_http_json's actual 2xx success contract. An
// invalid request never becomes accepted evidence. The server must bind loopback.
func SMSHandler(j *Journal, token string) (http.Handler, error) {
	if token == "" || j == nil {
		return nil, errors.New("SMS journal and token required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Recipient string `json:"recipient"`
			Message   string `json:"message"`
			Kind      string `json:"kind"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessage))
		d.DisallowUnknownFields()
		if err := d.Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF || !syntheticPhone(body.Recipient) || body.Message == "" || body.Kind == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := j.accept("sms", body.Recipient, body.Message); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"accepted":true}`)
	}), nil
}
func syntheticPhone(s string) bool { // Reserved fixture allocation, never a real subscriber.
	if !strings.HasPrefix(s, "+999") || len(s) < 10 || len(s) > 16 {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SMTP is a bounded, plaintext loopback-only RFC 5321 sink. No AUTH/STARTTLS is
// advertised: configure the production sender with tls_mode=none and no login.
type SMTP struct {
	listener net.Listener
	journal  *Journal
	mu       sync.Mutex
	conns    map[net.Conn]bool
	wg       sync.WaitGroup
	done     chan struct{}
	once     sync.Once
}

func StartSMTP(j *Journal) (*SMTP, error) {
	if j == nil {
		return nil, errors.New("SMTP journal required")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &SMTP{listener: l, journal: j, conns: map[net.Conn]bool{}, done: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			select {
			case <-s.done:
				s.mu.Unlock()
				c.Close()
				return
			default:
			}
			s.conns[c] = true
			s.wg.Add(1)
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s, nil
}
func (s *SMTP) Addr() string { return s.listener.Addr().String() }
func (s *SMTP) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.listener.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
	return nil
}
func (s *SMTP) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	rw := bufio.NewReadWriter(bufio.NewReaderSize(c, 4096), bufio.NewWriter(c))
	tp := textproto.NewReader(rw.Reader)
	reply := func(code int, msg string) bool {
		_, err := fmt.Fprintf(rw, "%d %s\r\n", code, msg)
		if err == nil {
			err = rw.Flush()
		}
		return err == nil
	}
	if !reply(220, "g09 fixture ready") {
		return
	}
	hello, mail := false, false
	var recipients []string
	for {
		lineBytes, err := rw.ReadSlice('\n')
		if err != nil {
			return
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(lineBytes), "\n"), "\r")
		command, arg, _ := strings.Cut(line, " ")
		command = strings.ToUpper(command)
		switch command {
		case "EHLO", "HELO":
			hello = true
			mail = false
			recipients = nil
			if !reply(250, "g09 fixture") {
				return
			}
		case "MAIL":
			if !hello || !strings.HasPrefix(strings.ToUpper(arg), "FROM:") {
				if !reply(503, "send HELO then MAIL") {
					return
				}
				continue
			}
			mail = true
			recipients = nil
			if !reply(250, "sender accepted") {
				return
			}
		case "RCPT":
			a := strings.TrimSpace(strings.TrimPrefix(arg, "TO:"))
			if !mail || !strings.HasPrefix(strings.ToUpper(arg), "TO:") || !strings.HasSuffix(strings.ToLower(a), ".test>") {
				if !reply(550, "fixture .test recipients only") {
					return
				}
				continue
			}
			if len(recipients) >= 10 {
				reply(452, "recipient limit")
				continue
			}
			recipients = append(recipients, a)
			if !reply(250, "recipient accepted") {
				return
			}
		case "DATA":
			if len(recipients) == 0 {
				if !reply(503, "recipient required") {
					return
				}
				continue
			}
			if !reply(354, "end with dot") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(tp.DotReader(), maxMessage+1))
			if err != nil || len(body) > maxMessage {
				return
			}
			for _, recipient := range recipients {
				if err = s.journal.accept("email", recipient, string(body)); err != nil {
					reply(451, "journal unavailable")
					return
				}
			}
			mail = false
			recipients = nil
			if !reply(250, "persisted") {
				return
			}
		case "RSET":
			mail = false
			recipients = nil
			if !reply(250, "reset") {
				return
			}
		case "NOOP":
			if !reply(250, "ok") {
				return
			}
		case "QUIT":
			reply(221, "bye")
			return
		default:
			if !reply(502, "unsupported") {
				return
			}
		}
	}
}
