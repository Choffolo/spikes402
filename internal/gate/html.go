package gate

import (
	"encoding/base64"
	"html/template"
	"io"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// The page shown to people who open a protected link in a browser.
// No JavaScript, no external resources.
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
</style>
</head>
<body>
<h1>Pay or PoW</h1>
<p>This page costs <strong>{{.PriceSats}} sats</strong>.</p>
{{with .L402}}
<p>Pay this Lightning invoice:</p>
<a href="lightning:{{.Invoice}}"><img src="{{$.QR}}" alt="QR code of the Lightning invoice" width="256" height="256"></a>
<p><code>{{.Invoice}}</code></p>
{{else}}
<p>Lightning payments are unavailable right now.</p>
{{end}}
<p><small>Bots and scripts can also solve a proof-of-work puzzle instead of paying
(difficulty {{.PoW.Difficulty}}, about {{.PoW.ExpectedSeconds}} seconds on one CPU core). Details are in the <code>WWW-Authenticate</code> headers of this response.
This offer expires at {{.ExpiresAt.Format "15:04 MST"}}.</small></p>
<p><small>Protected by <a href="https://github.com/Choffolo/spikes402">Spikes402</a>.</small></p>
</body>
</html>
`))

func writeHTML(w io.Writer, b challengeBody) {
	data := struct {
		challengeBody
		QR template.URL
	}{challengeBody: b}
	if b.L402 != nil {
		// Wallets expect upper-case BOLT11 in QR codes (smaller code).
		if png, err := qrcode.Encode(strings.ToUpper("lightning:"+b.L402.Invoice), qrcode.Medium, 256); err == nil {
			data.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
	}
	page.Execute(w, data)
}
