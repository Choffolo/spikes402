package gate

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"io"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// The page shown to people who open a protected link in a browser.
// No external resources. A small inline script talks to a WebLN wallet
// if the browser has one (for example Alby); otherwise the page is just
// the invoice and the QR code.
var page = template.Must(template.New("402").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>402 Payment Required · Spikes402</title>
<style>
 body{font-family:system-ui,sans-serif;max-width:34rem;margin:2rem auto;padding:0 1rem;color:#222}
 code{word-break:break-all;background:#f3f3f3;padding:.2rem .3rem;border-radius:4px;font-size:.85rem}
 img{display:block;margin:1rem 0;image-rendering:pixelated}
 small{color:#666}
 #webln{margin:1.2rem 0}
 #webln button{font:inherit;padding:.4rem .8rem}
 #webln .err{color:#a00}
</style>
</head>
<body>
<h1>Pay or PoW</h1>
<p>This page costs <strong>{{.PriceSats}} sats</strong>.</p>
{{with .L402}}
<p>Pay this Lightning invoice:</p>
<a href="lightning:{{.Invoice}}"><img src="{{$.QR}}" alt="QR code of the Lightning invoice" width="256" height="256"></a>
<p><code>{{.Invoice}}</code></p>
<p id="webln" hidden>
  <button type="button" id="webln-pay" data-invoice="{{.Invoice}}" data-token="{{.Token}}">Pay with one click</button>
  <small id="webln-msg"></small>
</p>
{{else}}
<p>Lightning payments are unavailable right now.</p>
{{end}}
<p><small>Bots and scripts can also solve a proof-of-work puzzle instead of paying
(difficulty {{.PoW.Difficulty}}, about {{.PoW.ExpectedSeconds}} seconds on one CPU core). Details are in the <code>WWW-Authenticate</code> headers of this response.
This offer expires at {{.ExpiresAt.Format "15:04 MST"}}.</small></p>
<p><small>Protected by <a href="https://github.com/Choffolo/spikes402">Spikes402</a>.</small></p>
{{.Script}}
</body>
</html>
`))

// weblnJS is the only script on the 402 page. It is written as a raw
// string so the Content-Security-Policy hash matches the bytes the
// browser sees. Invoice and token are read from the button, not
// interpolated here.
const weblnJS = `(function(){
var w=window.webln,box=document.getElementById("webln");
if(!w||!box)return;
box.hidden=false;
var btn=document.getElementById("webln-pay"),msg=document.getElementById("webln-msg"),busy=false;
function say(t,err){msg.textContent=t||"";msg.className=err?"err":""}
function fail(e){
  busy=false;btn.disabled=false;
  var m=(e&&(e.message||e.reason)||e||"")+"";
  say(/reject|denied|cancel|user/i.test(m)?"Payment was cancelled or the wallet was declined. You can still pay the invoice above.":(m||"Payment failed."),"err");
}
function pay(){
  if(busy)return;
  busy=true;btn.disabled=true;say("Requesting payment…");
  Promise.resolve(w.enable()).then(function(){return w.sendPayment(btn.getAttribute("data-invoice"))}).then(function(res){
    var preimage=((res&&(res.preimage||res.paymentPreimage))||"").toString().replace(/^0x/i,"").replace(/\s/g,"");
    if(!preimage)throw new Error("The wallet did not return a preimage.");
    return fetch(location.href,{headers:{Authorization:"L402 "+btn.getAttribute("data-token")+":"+preimage},credentials:"same-origin"}).then(function(r){
      if(!r.ok)throw new Error("Payment was sent, but the page did not accept it ("+r.status+").");
      return r.text();
    });
  }).then(function(html){
    document.open();document.write(html);document.close();
  }).catch(fail);
}
btn.addEventListener("click",pay);
pay();
})();`

var weblnHash = base64.StdEncoding.EncodeToString(sha256Sum([]byte(weblnJS)))

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func writeHTML(w io.Writer, b challengeBody) {
	data := struct {
		challengeBody
		QR     template.URL
		Script template.HTML
	}{challengeBody: b}
	if b.L402 != nil {
		// Wallets expect upper-case BOLT11 in QR codes (smaller code).
		if png, err := qrcode.Encode(strings.ToUpper("lightning:"+b.L402.Invoice), qrcode.Medium, 256); err == nil {
			data.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
		data.Script = template.HTML("<script>" + weblnJS + "</script>")
	}
	page.Execute(w, data)
}
