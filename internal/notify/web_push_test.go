package notify

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestWebPushSubscriptionRejectsUntrustedDestinationsAndMalformedKeys(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/opaque",
		P256DH:   base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	}
	for _, endpoint := range []string{
		"https://fcm.googleapis.com/fcm/send/opaque", "https://updates.push.services.mozilla.com/wpush/v2/opaque",
		"https://push.services.mozilla.com/wpush/opaque", "https://web.push.apple.com/opaque",
		"https://wns2-blah.notify.windows.com/w/?token=opaque",
	} {
		sub.Endpoint = endpoint
		if !ValidWebPushSubscription(sub) {
			t.Fatalf("supported vendor rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://fcm.googleapis.com/x", "https://127.0.0.1/x", "https://[::1]/x", "https://192.168.1.1/x",
		"https://attacker.example/x", "https://fcm.googleapis.com.attacker.example/x",
		"https://attacker-notify.windows.com/x", "https://notify.windows.com.attacker.example/x",
		"https://fcm.googleapis.com:443/x", "https://user@fcm.googleapis.com/x",
		"https://fcm.googleapis.com/x#fragment", "https://fcm.googleapis.com./x",
		"https://fcm.googleapis.com/", "https://fcm.googleapis.com", " https://fcm.googleapis.com/x",
		"https://fcm.googleapis.com/x\n", "https://fcm.googleapis.com/" + strings.Repeat("x", 4096),
	} {
		sub.Endpoint = endpoint
		if ValidWebPushSubscription(sub) {
			t.Fatal("unsafe push endpoint accepted")
		}
	}
	sub.Endpoint = "https://fcm.googleapis.com/fcm/send/opaque"
	for _, mutate := range []func(*PushSubscription){
		func(s *PushSubscription) { s.P256DH += "=" },
		func(s *PushSubscription) { s.P256DH = base64.RawURLEncoding.EncodeToString(make([]byte, 65)) },
		func(s *PushSubscription) { s.P256DH = base64.RawURLEncoding.EncodeToString(make([]byte, 33)) },
		func(s *PushSubscription) { s.Auth += "=" },
		func(s *PushSubscription) { s.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 15)) },
	} {
		copy := sub
		mutate(&copy)
		if ValidWebPushSubscription(copy) {
			t.Fatal("malformed browser key accepted")
		}
	}
}
