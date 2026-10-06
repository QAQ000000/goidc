package fileformat

import "testing"

func TestTicketMIMECharsetAndActiveContent(t *testing.T) {
	for _, c := range []struct {
		name, declared, body string
		want                 bool
	}{
		{"日志.txt", "", "故障日志\n", true}, {"binary.bin", "application/octet-stream", "\x00\x01\x02\x03", true},
		{"unsafe.txt", "text/plain", "<html><script>alert(1)</script></html>", false}, {"dangerous.php", "text/plain", "plain", false},
		{"dangerous.svg", "image/svg+xml", "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>", false},
	} {
		_, valid := TicketMIME(c.name, c.declared, []byte(c.body))
		if valid != c.want {
			t.Fatal("MIME validation", c.name, valid, c.want)
		}
	}
}
