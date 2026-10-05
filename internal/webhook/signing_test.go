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

type signingMarshalGate struct {
	entered chan struct{}
	release chan struct{}
}

func (g signingMarshalGate) MarshalJSON() ([]byte, error) {
	close(g.entered)
	<-g.release
	return []byte(`"gated"`), nil
}

func TestDispatchKeepsSigningSecretWithDestination(t *testing.T) {
	const oldSecret = "old-test-key"
	const newSecret = "new-test-key"
	type delivery struct {
		body      []byte
		signature string
	}
	oldDeliveries := make(chan delivery, 2)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		oldDeliveries <- delivery{body, r.Header.Get("X-WrongTrace-Signature")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer old.Close()
	newDeliveries := make(chan delivery, 1)
	newer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		newDeliveries <- delivery{body, r.Header.Get("X-WrongTrace-Signature")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer newer.Close()

	awaitDelivery := func() delivery {
		t.Helper()
		select {
		case got := <-oldDeliveries:
			return got
		case <-time.After(3 * time.Second):
			t.Fatal("old destination did not receive delivery")
			return delivery{}
		}
	}
	checkSignature := func(got delivery) {
		t.Helper()
		mac := hmac.New(sha256.New, []byte(oldSecret))
		mac.Write(got.body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got.signature != want {
			t.Errorf("old destination signature = %q, want signature using its original key %q", got.signature, want)
		}
	}

	d := NewDispatcher(Config{GenericURL: old.URL, SigningSecret: oldSecret, Timeout: 2 * time.Second})
	d.Dispatch(Payload{EventType: EventGuardrailBlock, Message: "control"})
	checkSignature(awaitDelivery())

	gate := signingMarshalGate{make(chan struct{}), make(chan struct{})}
	d.Dispatch(Payload{EventType: EventGuardrailBlock, Message: "gated", Details: map[string]interface{}{"barrier": gate}})
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("JSON encoder did not reach the gate")
	}
	d.UpdateConfig(Config{GenericURL: newer.URL, SigningSecret: newSecret, Timeout: 2 * time.Second})
	close(gate.release)
	checkSignature(awaitDelivery())
	select {
	case <-newDeliveries:
		t.Error("in-flight delivery switched destination")
	default:
	}

	d.Dispatch(Payload{EventType: EventGuardrailBlock, Message: "after update"})
	select {
	case got := <-newDeliveries:
		mac := hmac.New(sha256.New, []byte(newSecret))
		mac.Write(got.body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got.signature != want {
			t.Errorf("new destination signature = %q, want updated key signature %q", got.signature, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new destination did not receive delivery after update")
	}
}
