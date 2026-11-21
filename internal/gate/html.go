package gate

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/Choffolo/spikes402/pow"
)

// The page shown to people who open a protected link in a browser.
// No external resources, no external scripts. Two small inline scripts:
// one talks to a WebLN wallet if the browser has one (for example Alby),
// the other runs the proof-of-work puzzle in Web Workers. Without
// JavaScript the page is still the invoice, the QR code and the text.
var page = template.Must(template.New("402").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{with .Preview}}{{with .Title}}{{.}} · {{end}}{{end}}402 Payment Required · Spikes402</title>
{{with .Preview}}{{with .Description}}<meta name="description" content="{{.}}">
{{end}}{{end}}{{.LD}}
<style>
 body{font-family:system-ui,sans-serif;max-width:34rem;margin:2rem auto;padding:0 1rem;color:#222;line-height:1.45}
 code{word-break:break-all;background:#f3f3f3;padding:.2rem .3rem;border-radius:4px;font-size:.85rem}
 img{display:block;margin:1rem 0;image-rendering:pixelated}
 small{color:#666}
 article{border-left:4px solid #f7931a;padding:.1rem 1rem;margin:1rem 0;background:#fafafa}
 article h2{margin:.6rem 0 .3rem;font-size:1.1rem}
 .paywall{border-top:1px solid #ddd;margin-top:1.5rem}
 #wallet-box,#pow-box{margin:1.2rem 0}
 #wallet-box button,#pow-box button{font:inherit;padding:.4rem .8rem}
 #pow-bar{display:block;width:100%;height:1.2rem;margin:.8rem 0 .3rem}
 .err{color:#a00}
</style>
</head>
<body>
<h1>An invisible energy wall</h1>
<p>This page is protected by an invisible energy wall built with Bitcoin.
Pay a few sats with the <a href="https://github.com/Choffolo/spikes402#browser-wallet-extension-prototype">Spikes402 extension</a>,
or solve a math puzzle with your computer&rsquo;s power.</p>
{{with .Preview}}<article>
{{with .Title}}<h2>{{.}}</h2>
{{end}}{{with .Description}}<p>{{.}}</p>
{{end}}{{with .Excerpt}}<p>{{.}}</p>
{{end}}<p><small>A taste of what is behind the wall.</small></p>
</article>
{{end}}<div class="paywall">
<p>This page costs <strong>{{.PriceSats}} sats</strong>.</p>
{{if .Solve}}<h2>Solve the puzzle</h2>
<p>Your browser searches for a number that unlocks this page: about {{.Attempts}} attempts, using your CPU.
<noscript>This needs JavaScript.</noscript></p>
<div id="pow-box" hidden data-challenge="{{.PoW.Challenge}}" data-difficulty="{{.PoW.Difficulty}}" data-worker="{{.WorkerSrc}}">
  <button type="button" id="pow-go">Solve the puzzle</button>
  <progress id="pow-bar" max="1" value="0" hidden></progress>
  <small id="pow-stats" aria-live="polite"></small>
  <p id="pow-msg" role="status"></p>
</div>
{{end}}<h2>{{if .Solve}}Or pay{{else}}Pay{{end}} with Lightning</h2>
<p>The <a href="https://github.com/Choffolo/spikes402#browser-wallet-extension-prototype">Spikes402 extension</a> pays in one click, with no account.
Or pay this invoice with any Lightning wallet:</p>
{{with .L402}}
<a href="lightning:{{.Invoice}}"><img src="{{$.QR}}" alt="QR code of the Lightning invoice" width="256" height="256"></a>
<p><code>{{.Invoice}}</code></p>
<p id="wallet-box" hidden>
  <button type="button" id="webln-pay" data-invoice="{{.Invoice}}" data-token="{{.Token}}">Pay with one click</button>
  <small id="webln-msg"></small>
</p>
{{else}}
<p>Lightning payments are unavailable right now.</p>
{{end}}
<p><small>Bots and scripts: the puzzle (difficulty {{.PoW.Difficulty}}, about {{.PoW.ExpectedSeconds}} seconds on one CPU core)
is described in the <code>WWW-Authenticate</code> headers of this response.
This offer expires at {{.ExpiresAt.Format "15:04 MST"}}.</small></p>
</div>
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
var w=window.webln,box=document.getElementById("wallet-box");
if(!box||!w||typeof w.enable!=="function"||typeof w.sendPayment!=="function")return;
box.hidden=false;
var btn=document.getElementById("webln-pay"),msg=document.getElementById("webln-msg"),busy=false,KEY="spikes402-redeemed";
function say(t,err){msg.textContent=t||"";msg.className=err?"err":""}
function fail(e){
  busy=false;btn.disabled=false;
  var m=(e&&(e.message||e.reason)||e||"")+"";
  say(/reject|denied|cancel|user/i.test(m)?"Payment was cancelled or the wallet was declined. You can still pay the invoice above.":(m||"Payment failed."),"err");
}
function pay(){
  if(busy)return;
  busy=true;btn.disabled=true;say("Requesting payment…");
  Promise.resolve().then(function(){return w.enable()}).then(function(){return w.sendPayment(btn.getAttribute("data-invoice"))}).then(function(res){
    var preimage=((res&&(res.preimage||res.paymentPreimage))||"").toString().replace(/^0x/i,"").replace(/\s/g,"");
    if(!preimage)throw new Error("The wallet did not return a preimage.");
    return fetch(location.href,{headers:{Authorization:"L402 "+btn.getAttribute("data-token")+":"+preimage,"Spikes402-Redeem":"cookie"},credentials:"same-origin",cache:"no-store"}).then(function(r){
      if(r.status!==204)throw new Error("Payment was sent, but the page did not accept it ("+r.status+").");
      try{sessionStorage.setItem(KEY,Date.now())}catch(e){}
      location.replace(location.href);
    });
  }).catch(fail);
}
btn.addEventListener("click",pay);
var last=0;try{last=+sessionStorage.getItem(KEY)||0}catch(e){}
if(Date.now()-last<50000){say("Payment was sent, but the browser did not keep the access cookie. Allow cookies for this site and try again in a minute.",true)}
else pay();
})();`

var weblnHash = base64.StdEncoding.EncodeToString(sha256Sum([]byte(weblnJS)))

// wall.js runs the puzzle in the browser; pow-worker.js is the worker it
// starts, served by the proxy itself (see serveWorker). Both are embedded
// so the page needs nothing from outside.
var (
	//go:embed assets/wall.js
	wallJS string
	//go:embed assets/pow-worker.js
	powWorkerJS string

	wallHash = base64.StdEncoding.EncodeToString(sha256Sum([]byte(wallJS)))

	// workerSrc carries a content hash so a new release is never served
	// from an old cached copy.
	workerSrc = WorkerPath + "?v=" + hex.EncodeToString(sha256Sum([]byte(powWorkerJS))[:6])

	// pageCSP: nothing from outside, two known inline scripts, and workers
	// only from this origin.
	pageCSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'sha256-" + weblnHash + "' 'sha256-" + wallHash +
		"'; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
)

// WorkerPath is where the proxy serves the puzzle worker. It is reserved:
// it is answered before any protect rule is looked at, and never reaches
// the origin.
const WorkerPath = "/.spikes402/pow-worker.js"

func serveWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Cache-Control", "public, max-age=86400")
	if r.Method == http.MethodGet {
		io.WriteString(w, powWorkerJS)
	}
}

// ldJSON is the schema.org markup Google asks for on paywalled pages: the
// page is not free, and the part behind the wall is the ".paywall" block.
// json.Marshal escapes <, > and &, so the text cannot close the script.
func ldJSON(p *Preview) template.HTML {
	type part struct {
		Type        string `json:"@type"`
		Free        bool   `json:"isAccessibleForFree"`
		CSSSelector string `json:"cssSelector"`
	}
	doc := struct {
		Context     string `json:"@context"`
		Type        string `json:"@type"`
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
		Free        bool   `json:"isAccessibleForFree"`
		HasPart     part   `json:"hasPart"`
	}{Context: "https://schema.org", Type: "WebPage", HasPart: part{"WebPageElement", false, ".paywall"}}
	if p != nil {
		doc.Name, doc.Description = p.Title, p.Description
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	return template.HTML(`<script type="application/ld+json">` + string(b) + `</script>`)
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// invoiceQR is a data URI of the Lightning invoice as a QR code.
// Wallets expect upper-case BOLT11 in QR codes (smaller code).
func invoiceQR(invoice string) template.URL {
	png, err := qrcode.Encode(strings.ToUpper("lightning:"+invoice), qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
}

func writeHTML(w io.Writer, b challengeBody, qr template.URL) {
	data := struct {
		challengeBody
		QR        template.URL
		Script    template.HTML
		LD        template.HTML
		Solve     bool   // the browser can solve this puzzle (not Argon2id)
		Attempts  string // expected attempts, for the page text
		WorkerSrc string
	}{challengeBody: b, QR: qr, LD: ldJSON(b.Preview), WorkerSrc: workerSrc}
	var scripts string
	if b.L402 != nil {
		scripts += "<script>" + weblnJS + "</script>\n"
	}
	if data.Solve = b.PoW.Algorithm != pow.Argon2id; data.Solve {
		data.Attempts = humanCount(math.Exp2(float64(b.PoW.Difficulty)))
		scripts += "<script>" + wallJS + "</script>\n"
	}
	data.Script = template.HTML(scripts)
	page.Execute(w, data)
}

// humanCount writes n roughly: "65 thousand", "34 million".
func humanCount(n float64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.3g billion", n/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.3g million", n/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.3g thousand", n/1e3)
	}
	return fmt.Sprintf("%.0f", n)
}
