package lightning

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePhoenixd mimics the phoenixd endpoints and its two passwords.
func fakePhoenixd(t *testing.T, sendAnswer string) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, _ := r.BasicAuth()
		r.ParseForm()
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Form.Encode())
		full := pw == "full"
		if !full && pw != "limited" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/getinfo":
			w.Write([]byte(`{"nodeId":"02abc","channels":[{},{}],"chain":"mainnet","blockHeight":1,"version":"0.6.0"}`))
		case "/getbalance":
			w.Write([]byte(`{"balanceSat":1234,"feeCreditSat":5}`))
		case "/paylnaddress", "/sendtoaddress":
			if !full {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(sendAnswer))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestPhoenixdNodeInfo(t *testing.T) {
	srv, _ := fakePhoenixd(t, "")
	info, err := (&Phoenixd{URL: srv.URL, Password: "limited"}).NodeInfo(context.Background())
	if err != nil || info.NodeID != "02abc" || info.Channels != 2 || info.BalanceSat != 1234 || info.FeeCreditSat != 5 || info.Chain != "mainnet" {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := (&Phoenixd{URL: srv.URL, Password: "wrong"}).NodeInfo(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPhoenixdWithdrawLightningAddress(t *testing.T) {
	srv, calls := fakePhoenixd(t, `{"recipientAmountSat":1000,"routingFeeSat":3,"paymentId":"uuid-1","paymentHash":"aa","paymentPreimage":"bb"}`)
	ref, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full"}).Withdraw(context.Background(), "me@wallet.example", 1000)
	if err != nil || !strings.Contains(ref, "uuid-1") {
		t.Fatalf("%q %v", ref, err)
	}
	if got := (*calls)[0]; !strings.Contains(got, "POST /paylnaddress") || !strings.Contains(got, "address=me%40wallet.example") || !strings.Contains(got, "amountSat=1000") {
		t.Fatalf("call %q", got)
	}
	// The limited password cannot spend.
	if _, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "limited"}).Withdraw(context.Background(), "me@wallet.example", 1000); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPhoenixdWithdrawFailures(t *testing.T) {
	srv, _ := fakePhoenixd(t, `{"paymentHash":null,"offerId":null,"reason":"no route"}`)
	if _, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full"}).Withdraw(context.Background(), "me@wallet.example", 1000); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("got %v", err)
	}
	srv, _ = fakePhoenixd(t, `insufficient funds`)
	if _, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full", FeerateSatByte: 2}).Withdraw(context.Background(), "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", 1000); err == nil {
		t.Fatal("error text accepted as a txid")
	}
	if _, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full"}).Withdraw(context.Background(), "not an address", 1000); err == nil {
		t.Fatal("bad destination accepted")
	}
}

func TestPhoenixdWithdrawOnChain(t *testing.T) {
	txid := strings.Repeat("ab", 32)
	srv, calls := fakePhoenixd(t, txid)
	ref, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full", FeerateSatByte: 4}).Withdraw(context.Background(), "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", 50000)
	if err != nil || ref != "txid "+txid {
		t.Fatalf("%q %v", ref, err)
	}
	if got := (*calls)[0]; !strings.Contains(got, "/sendtoaddress") || !strings.Contains(got, "feerateSatByte=4") {
		t.Fatalf("call %q", got)
	}
}

func TestAddressChecks(t *testing.T) {
	for _, s := range []string{"me@wallet.example", "a.b+c@x.y.z"} {
		if !IsLightningAddress(s) {
			t.Errorf("%s rejected", s)
		}
	}
	for _, s := range []string{"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"} {
		if !IsBitcoinAddress(s) {
			t.Errorf("%s rejected", s)
		}
	}
	for _, s := range []string{"", "me@localhost", "me@x.y\n", "bc1 space", "lnbc1...", "0xdeadbeef", "@x.y"} {
		if IsLightningAddress(s) || IsBitcoinAddress(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestFakeBalanceAndWithdraw(t *testing.T) {
	f := NewFake()
	inv, _ := f.CreateInvoice(context.Background(), 100, "", time.Minute)
	if _, err := f.Pay(inv.PaymentRequest); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.NodeInfo(context.Background()); info.BalanceSat != 100 {
		t.Fatalf("balance %d", info.BalanceSat)
	}
	if _, err := f.Withdraw(context.Background(), "me@x.y", 101); err == nil {
		t.Fatal("overdraft accepted")
	}
	if _, err := f.Withdraw(context.Background(), "me@x.y", 60); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.NodeInfo(context.Background()); info.BalanceSat != 40 || len(f.Withdrawals()) != 1 {
		t.Fatalf("balance %d", info.BalanceSat)
	}
}

func TestNormalizeDestination(t *testing.T) {
	for in, want := range map[string]string{
		"₿me@wallet.example": "me@wallet.example", "  ₿ me@wallet.example \n": "me@wallet.example",
		"me@wallet.example": "me@wallet.example", "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
	} {
		if got := NormalizeDestination(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
	// The "₿" form is sent to phoenixd without the symbol.
	srv, calls := fakePhoenixd(t, `{"recipientAmountSat":10,"routingFeeSat":4,"paymentId":"uuid-2"}`)
	if _, err := (&PhoenixdWithdrawer{URL: srv.URL, FullPassword: "full"}).Withdraw(context.Background(), " ₿me@wallet.example", 10); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[0]; !strings.Contains(got, "address=me%40wallet.example&") {
		t.Fatalf("call %q", got)
	}
}

func TestEstimatedFee(t *testing.T) {
	ln := "me@wallet.example"
	for amount, want := range map[int64]int64{1: 5, 250: 5, 251: 6, 3000: 16, 100_000: 404} {
		if got := EstimatedFeeSat(ln, amount); got != want {
			t.Errorf("%d: fee %d, want %d", amount, got, want)
		}
	}
	if f := EstimatedFeeSat("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", 3000); f != 0 {
		t.Errorf("on-chain fee %d", f)
	}
	for _, bal := range []int64{0, 4, 5, 6, 1000, 5544, 1_000_000} {
		m := MaxWithdrawSat(ln, bal)
		if m > 0 && m+EstimatedFeeSat(ln, m) > bal || m+1+EstimatedFeeSat(ln, m+1) <= bal {
			t.Errorf("balance %d: max %d", bal, m)
		}
	}
	if m := MaxWithdrawSat("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", 5544); m != 5544 {
		t.Errorf("on-chain max %d", m)
	}
}

func TestPhoenixdWithdrawUncertain(t *testing.T) {
	w := func(url string) error {
		_, err := (&PhoenixdWithdrawer{URL: url, FullPassword: "full"}).Withdraw(context.Background(), "me@wallet.example", 10)
		return err
	}
	// A clear "no" from phoenixd is a failure, not an unknown.
	srv, _ := fakePhoenixd(t, `{"reason":"no route"}`)
	if err := w(srv.URL); err == nil || errors.Is(err, ErrUncertain) {
		t.Fatalf("got %v", err)
	}
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cannot resolve address", http.StatusInternalServerError)
	}))
	defer fail.Close()
	if err := w(fail.URL); err == nil || errors.Is(err, ErrUncertain) {
		t.Fatalf("got %v", err)
	}
	// Nothing listening: the payment never started.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if err := w(dead.URL); err == nil || errors.Is(err, ErrUncertain) {
		t.Fatalf("got %v", err)
	}
	// A broken answer or no answer in time: the money may have left.
	srv, _ = fakePhoenixd(t, `<html>gateway</html>`)
	if err := w(srv.URL); !errors.Is(err, ErrUncertain) {
		t.Fatalf("got %v", err)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := (&PhoenixdWithdrawer{URL: slow.URL, FullPassword: "full"}).Withdraw(ctx, "me@wallet.example", 10)
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("got %v", err)
	}
}
