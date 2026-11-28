package lightning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFakePay(t *testing.T) {
	f := NewFake()
	inv, err := f.CreateInvoice(context.Background(), 10, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	preimageHex, err := f.Pay(inv.PaymentRequest)
	if err != nil {
		t.Fatal(err)
	}
	preimage, _ := hex.DecodeString(preimageHex)
	if sha256.Sum256(preimage) != inv.PaymentHash {
		t.Fatal("preimage does not match payment hash")
	}
	if _, err := f.Pay(inv.PaymentRequest); err != ErrUnknownInvoice {
		t.Fatalf("paying twice: got %v", err)
	}
	if _, err := f.Pay("lnfake10sat1" + hex.EncodeToString(make([]byte, 32))); err != ErrUnknownInvoice {
		t.Fatalf("unknown invoice: got %v", err)
	}
}

func TestFakeExpiryAndMemory(t *testing.T) {
	f := NewFake()
	now := time.Unix(0, 0)
	f.now = func() time.Time { return now }
	inv, _ := f.CreateInvoice(context.Background(), 1, "", time.Minute)
	now = now.Add(time.Minute)
	if _, err := f.Pay(inv.PaymentRequest); err != ErrUnknownInvoice {
		t.Fatalf("expired invoice: got %v", err)
	}
	for i := 0; i < 5000; i++ {
		f.CreateInvoice(context.Background(), 1, "", time.Minute)
		now = now.Add(time.Second)
	}
	if n := len(f.invoices); n > 1100 {
		t.Fatalf("%d invoices kept in memory", n)
	}
}

func TestPhoenixdCreateInvoice(t *testing.T) {
	const hash = "5d1d4fd1fa2bb8e9c0c4a0c8bb5bd7b49b1e2b6e3c8f0f7c5a1a0e2d3c4b5a69"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if r.URL.Path != "/createinvoice" || r.Method != http.MethodPost || !ok || pass != "secret" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		if r.FormValue("amountSat") != "21" || r.FormValue("description") != "memo" || r.FormValue("expirySeconds") != "600" {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"amountSat":21,"paymentHash":"` + hash + `","serialized":"lnbc210n1test"}`))
	}))
	defer srv.Close()

	p := &Phoenixd{URL: srv.URL, Password: "secret"}
	inv, err := p.CreateInvoice(context.Background(), 21, "memo", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if inv.PaymentRequest != "lnbc210n1test" || hex.EncodeToString(inv.PaymentHash[:]) != hash {
		t.Fatalf("unexpected invoice %+v", inv)
	}

	p.Password = "wrong"
	if _, err := p.CreateInvoice(context.Background(), 21, "memo", 10*time.Minute); err == nil {
		t.Fatal("expected error with wrong password")
	}
}

func TestFakeLookupIncoming(t *testing.T) {
	f := NewFake()
	inv, _ := f.CreateInvoice(context.Background(), 10, "test", time.Minute)
	got, err := f.LookupIncoming(context.Background(), inv.PaymentHash)
	if err != nil || got.Paid || got.Expired {
		t.Fatalf("unpaid: %+v, %v", got, err)
	}
	preimage, err := f.Pay(inv.PaymentRequest)
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.LookupIncoming(context.Background(), inv.PaymentHash)
	if err != nil || !got.Paid || hex.EncodeToString(got.Preimage[:]) != preimage || got.ReceivedSat != 10 {
		t.Fatalf("paid: %+v, %v", got, err)
	}
	var missing [32]byte
	if _, err := f.LookupIncoming(context.Background(), missing); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPhoenixdLookupIncomingPaidAndUnpaid(t *testing.T) {
	preimage := bytesOf(32, 7)
	sum := sha256.Sum256(preimage)
	hash := hex.EncodeToString(sum[:])
	pre := hex.EncodeToString(preimage)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if r.URL.Path != "/payments/incoming/"+hash || r.Method != http.MethodGet || !ok || pass != "secret" {
			if r.URL.Path == "/payments/incoming/"+hex.EncodeToString(make([]byte, 32)) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Unpaid") == "1" || r.URL.RawQuery == "unpaid" {
			w.Write([]byte(`{"paymentHash":"` + hash + `","isPaid":false,"isExpired":true,"receivedSat":0}`))
			return
		}
		w.Write([]byte(`{"paymentHash":"` + hash + `","isPaid":true,"preimage":"` + pre + `","receivedSat":18,"fees":2500}`))
	}))
	defer srv.Close()

	// paid
	p := &Phoenixd{URL: srv.URL, Password: "secret"}
	got, err := p.LookupIncoming(context.Background(), sum)
	if err != nil || !got.Paid || got.ReceivedSat != 21 || got.Preimage != bytes32(preimage) { // 18 + 2.5 fee, rounded up
		t.Fatalf("paid: %+v, %v", got, err)
	}

	var missing [32]byte
	if _, err := p.LookupIncoming(context.Background(), missing); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func bytesOf(n, last byte) []byte {
	b := make([]byte, n)
	b[n-1] = last
	return b
}

func bytes32(b []byte) (out [32]byte) {
	copy(out[:], b)
	return out
}
