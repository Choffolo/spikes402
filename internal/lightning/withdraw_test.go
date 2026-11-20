package lightning

import (
	"context"
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
