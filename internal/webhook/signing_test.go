package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// receiveGenericDelivery dispatches one payload and captures the generic
// endpoint's signature header plus raw body from a one-shot test server.
func receiveGenericDelivery(t *testing.T, cfg Config) (string, []byte) {
	t.Helper()
	sigCh := make(chan string, 1)
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyCh <- b
		sigCh <- r.Header.Get("X-WrongTrace-Signature")
	}))
	defer srv.Close()
	cfg.GenericURL = srv.URL
	cfg.Timeout = time.Second

	NewDispatcher(cfg).Dispatch(Payload{EventType: EventThrashingAlert, Severity: "info", Message: "probe"})

	select {
	case sig := <-sigCh:
		return sig, <-bodyCh
	case <-time.After(2 * time.Second):
		t.Fatal("generic delivery never arrived")
		return "", nil
	}
}

func TestGenericDeliveryCarriesHMACSignature(t *testing.T) {
	const secret = "receiver-shared-secret"
	sig, body := receiveGenericDelivery(t, Config{SigningSecret: secret})
	if sig == "" {
		t.Fatal("X-WrongTrace-Signature missing on generic delivery")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Errorf("signature = %q, want %q (must be hex HMAC-SHA256 over the exact body)", sig, want)
	}
}

func TestGenericDeliveryUnsignedWithoutSecret(t *testing.T) {
	sig, _ := receiveGenericDelivery(t, Config{})
	if sig != "" {
		t.Errorf("unexpected X-WrongTrace-Signature %q when no secret configured", sig)
	}
}
