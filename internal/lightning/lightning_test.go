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
	if _, err := f.Pay("lnfake10sat1" + hex.EncodeToString(make([]byte, 32))); err != ErrUnknownInvoice {
		t.Fatalf("unknown invoice: got %v", err)
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
