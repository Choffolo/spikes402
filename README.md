# <img src="assets/logo.svg" alt="Spikes402" width="40" height="40"> Spikes402 — Pay or PoW

**Spikes402** is a small reverse proxy you put in front of a website. On the
paths you choose, AI bots and crawlers must either **pay** a few sats over
the Bitcoin Lightning Network ([L402](https://github.com/lightninglabs/L402))
or **do some work** (a proof-of-work puzzle, like Bitcoin mining in
miniature). Everything else passes through untouched.

> **Status: experimental.** This is a first prototype. Do not use it with
> real money on mainnet before an independent review.

- One request = one payment. Short access only, chosen by the site operator
  (0-60 second pass, default 10 seconds); never accounts or subscriptions.
- Non-custodial: payments go to *your own* Lightning node. Spikes402 never
  holds anyone's keys or funds.
- A single Go binary, two small dependencies (QR codes and `golang.org/x/crypto`
  for Argon2id), AGPLv3.
- Knows who is asking: bots that sign their requests
  ([Web Bot Auth](#bot-identification)) and well-known crawlers checked
  against their published IP lists. Impostors are flagged. By default
  everyone still pays the same price.
- Optional [admin dashboard](#admin-dashboard) with a live **siege monitor**,
  an [MCP server](#ai-admin-mcp) for an AI assistant, and optional
  [withdrawals](#withdrawals-optional-off-by-default) to one fixed address.

**Protect a site in about five minutes:** the [Docker quickstart](docs/quickstart.md) (proof-of-work only, no Lightning node). The [local Go quick start](#quick-start-fake-lightning-no-real-money) is further down.

## How it works

```mermaid
sequenceDiagram
    participant B as Bot or browser
    participant S as Spikes402
    participant W as Your website
    B->>S: GET /premium/article
    S-->>B: 402 Payment Required (Lightning invoice + PoW challenge)
    Note over B: Pay the invoice (get the preimage)<br/>or solve the puzzle (get a nonce)
    B->>S: GET /premium/article + Authorization: L402 token:preimage or PoW token:nonce
    S->>S: Check the proof (valid once only)
    S->>W: Forward the request
    W-->>S: Content
    S-->>B: 200 OK + content
```

Free paths are forwarded to your website directly, with no challenge.
Protected-path answers (the 402 itself, and the page after payment, work, a
pass, a pause, or a free policy) always go out with
`Cache-Control: private, no-store`, so a shared cache or CDN cannot serve a
paid page for free. Unprotected paths keep whatever `Cache-Control` your
website sets.

1. A request to a protected path without proof gets **HTTP 402**. The answer
   holds a signed **token**, a Lightning **invoice** for the price, and a
   **proof-of-work challenge** (the same token). A JSON body explains both
   options; browsers get a small page with a QR code instead. If a
   [WebLN](https://www.webln.guide/) wallet is in the browser, a "Pay with
   one click" button also appears, and the page tries to pay once on load
   with the invoice already shown (no second invoice). If the payment fits
   the wallet's budget, that can succeed without a further click. After
   paying, the page swaps the proof for a **pass**: a cookie (HttpOnly,
   SameSite=Strict, one page, `pass_ttl_seconds` long, default 10, at most
   60; 0 means no pass) and then loads the page for real, so
   its CSS, images and scripts work, files download normally, and a reload
   within that lifetime does not pay again. The same page also shows an **Open in
   wallet** link (`lightning:`) and, after you pay with any Lightning wallet
   (a phone scanning the QR code, for example), unlocks on its own: it asks
   `/.spikes402/claim` every 1.5 seconds whether that invoice is paid, then
   sets the pass and reloads. The claim request carries the signed token from
   the 402 page, so a third party who only sees the invoice cannot take the
   page.
2. **Pay:** paying the invoice reveals its *preimage*. The bot repeats the
   request with `Authorization: L402 <token>:<preimage>`. Spikes402 checks
   that `sha256(preimage)` equals the payment hash inside the token.
3. **Or work:** the bot finds a number `nonce` such that
   `SHA256(SHA256(token + ":" + nonce))` starts with `difficulty` zero bits,
   and sends `Authorization: PoW <token>:<nonce>`.
4. Each token is **bound to one resource** (method + path + query), **expires**
   (10 minutes by default) and can be **used once**, by either route.

## Quick start (fake Lightning, no real money)

You need [Go](https://go.dev/dl/) 1.26 or newer (builds use Go 1.27.4 or
later, which has the latest security fixes; `go` downloads it if needed).

```sh
git clone https://github.com/Choffolo/spikes402 && cd spikes402
go build -o bin/ ./cmd/...
bin/demo-origin & bin/spikes402 -config config.example.json & sleep 1
bin/spikes-bot -mode pow http://localhost:8402/premium/article
```

This starts a stand-in website on port 8080, the proxy on port 8402 in front
of it, and then the example bot. Stop the servers with `kill %1 %2`.

Expected output:

```
bot: GET http://localhost:8402/premium/article
bot: 402: solving sha256d proof-of-work (difficulty 24, ~8.4 s on one CPU core)...
bot: 402: solved, nonce=8717585
bot: 200 OK in 949ms
bot: the site classified us as: unknown
Premium content at /premium/article. Thank you for paying (or working)!
```

Try `-mode pay` to pay with the fake wallet instead, `curl -i
http://localhost:8402/premium/article` to see the raw 402 answer, or open
that URL in a browser to see the payment page.

## Configuration

A JSON file (see [`config.example.json`](config.example.json)):

| Key | Meaning | Default |
|---|---|---|
| `listen` | Address the proxy listens on | `":8402"` (`"127.0.0.1:8402"` with the fake backend) |
| `origin` | The website to protect (`http://` or `https://` URL) | required |
| `price_sats` | Default price of one request, in satoshis (used when no `prices` rule matches) | required, ≥ 1, at most 1,000,000 |
| `prices` | Optional list of `{ "pattern", "price_sats" }` rules. Same pattern syntax and canonical path as `protect`. **The first matching rule wins**; put more specific patterns first. Unmatched protected paths use `price_sats`. | `[]` |
| `surge` | Optional demand-based price multiplier, **off by default**. See [Dynamic pricing](#dynamic-pricing-optional-off-by-default). Config file only (not a dashboard control) | off |
| `previews` | Optional list of `{ "pattern", "mode", "max_chars" }` rules that show a teaser of the page on its 402 page, see [The 402 page](#the-402-page-for-people). `mode`: `"none"`, `"title"` (title and meta description) or `"excerpt"` (also the first `max_chars` characters, default 300, at most 1000). Same patterns as `protect`; **the first matching rule wins**. Config file only | `[]` = no teaser |
| `wall_mode` | 402 page layout. Omitted is the default two-door page (pay and puzzle side by side). `"pow_first"` starts the puzzle automatically (unless the visitor prefers reduced motion) and offers Lightning under “Tired of waiting? Pay this →”. The invoice is still created when the page is served. Config file only | omitted |
| `login_routes` | Login endpoints where every attempt costs an Argon2id proof-of-work that grows with each failure; see [Login protection](#login-protection). Config file only | `[]` |
| `login_surge_failures_per_minute` | Failed logins per minute, site-wide, that add one and two levels for everyone. Two increasing counts. Config file only | `[100, 1000]` |
| `protect` | Path patterns that require payment. `"/premium/**"` = everything under `/premium`; other patterns use [glob syntax](https://pkg.go.dev/path#Match), e.g. `"/*.pdf"`. See [Path matching](#path-matching) | required |
| `protect_case_sensitive` | Match `protect` patterns with exact letter case. Off by default, because many websites serve `/Premium` like `/premium` | `false` |
| `pow_seconds_per_sat` | How much work replaces one sat: seconds of one CPU core (see [Proof-of-work pricing](#proof-of-work-pricing)). Values below 1 are refused unless `unsafe_allow_dangerous_config` is set. Example configs use `5` with `price_sats: 2`, which is the same puzzle as 1 sat at this default | `10` |
| `pow_difficulty` | Leading zero bits for the puzzle. `0` = derive from the price and `pow_seconds_per_sat` | `0` |
| `pow_algorithm` | `"sha256d"` or `"argon2id"` (memory-hard, see [Argon2id](#argon2id-memory-hard-proof-of-work)) | `"sha256d"` |
| `argon2_memory_mib` | Memory per Argon2id attempt, 8–256 MiB | `16` |
| `argon2_max_memory_mib` | Memory all concurrent Argon2id checks of the API puzzle may use together (login checks have their own 128 MiB, see [Login protection](#login-protection)). A check that needs more than this is refused at startup. Extra checks wait briefly, then `503` | `256` |
| `challenge_ttl_seconds` | How long a 402 offer (token and invoice) stays valid | `600` |
| `pass_ttl_seconds` | How long the post-unlock access pass lasts (0-60). `0` means every request needs a fresh payment or proof-of-work | `10` |
| `challenges_per_minute` | 402 answers per client per minute; beyond that, `429 Too Many Requests` | `30` |
| `max_invoices_per_minute` | Invoices created per minute, all clients together; beyond that, proof-of-work only | `300` |
| `max_pending_invoices` | Open (unpaid, unexpired) invoices at once; beyond that, proof-of-work only | `10000` |
| `client_ip_header` | Header holding the visitor's IP when Spikes402 runs behind your own proxy, e.g. `"X-Forwarded-For"`, `"X-Real-IP"`, `"CF-Connecting-IP"` | `""` (use the connection's address) |
| `trusted_proxies` | IPs or CIDR ranges of those proxies, e.g. `["127.0.0.1"]`. The header is ignored from anyone else. Required with `client_ip_header` or `preserve_host` | `[]` |
| `preserve_host` | Send the public `Host` (or `X-Forwarded-Host`) to the origin, for name-based virtual hosts. Only from `trusted_proxies`; if `public_hosts` is set, only those names are forwarded. Off: the origin sees the host of `origin` | `false` |
| `max_connections` | Client connections open at once; more wait in the queue | `10000` |
| `read_timeout_seconds` / `write_timeout_seconds` | Longest time to receive a whole request / send a whole response (raise the second for very large downloads or streaming) | `60` / `300` |
| `unsafe_allow_dangerous_config` | Accept settings refused by default: `trusted_proxies` or `admin.allowed_ips` covering the whole internet (`0.0.0.0/0`, `::/0`), or the fake backend on a non-localhost address | `false` |
| `secret_key` | Signs tokens, ≥ 32 characters. Better set via `SPIKES402_SECRET_KEY` | random with `fake`, required otherwise |
| `state_dir` | Directory for spent tokens, withdrawal intents, and dashboard overrides (mode 0700, files 0600). Created on startup if missing. Docker: must be a writable volume | `<directory of the config file>/state` |
| `identify_bots` | Classify clients (Web Bot Auth, published IP lists, reverse DNS); see [Bot identification](#bot-identification) | `true` |
| `known_bots` | Replace the built-in list of crawlers: `[{"name", "user_agent", "ip_ranges_url", "reverse_dns": [...]}]` | built-in list |
| `public_stats` | Serve aggregate counters (unlocks and sats received, no visitor data) at `GET /.spikes402/stats` | `false` |
| `public_siege` | Serve a delayed, anonymised [public siege view](#public-siege-view-optional-off-by-default) at `/.spikes402/siege` and link it from the 402 page | `false` |
| `paused` | Serve protected paths free and create no invoices (dashboard toggle; also honoured in the file / overrides) | `false` |
| `bot_policies` | Optional per-class treatment (see [Policies](#policies-off-by-default-open-for-discussion)) | `{}` = everyone pays the same |
| `web_bot_auth_trusted_agents` | Domains whose Web Bot Auth key directories you trust, e.g. `["chatgpt.com"]` (subdomains included). Only those signatures make a request `verified-signed`; see [Web Bot Auth](#web-bot-auth) | `[]` = none |
| `public_hosts` | Your site's host names, e.g. `["example.com", "www.example.com"]`. Signatures made for any other host are refused. Required with `web_bot_auth_trusted_agents` | `[]` |
| `web_bot_auth_dev_mode` | Accept key directories on `http://`, localhost and any port, and signatures over plain http. **Testing only**; refused with a real Lightning backend | `false` |
| `admin.key` | Turns on the [admin dashboard](#admin-dashboard), ≥ 32 characters. Better set via `SPIKES402_ADMIN_KEY` | `""` = off |
| `admin.listen` | Address of the dashboard/API, separate from the proxy | `"127.0.0.1:8404"` |
| `admin.allowed_ips` | IPs/CIDRs allowed to reach the dashboard besides localhost. Required if `admin.listen` is not localhost | `[]` |
| `admin.ai_key` | Separate key for the [AI admin (MCP)](#ai-admin-mcp). Better set via `SPIKES402_AI_KEY` | `""` = no AI |
| `admin.ai_can_apply` | Let the AI apply changes that do not reduce protection; anything weaker stays a suggestion for you to approve | `false` |
| `admin.visitor_ips` | How visitors appear in the siege monitor: `"hash"`, `"truncate"` (/24, /48) or `"full"` | `"hash"` |
| `withdraw.*` | See [Withdrawals](#withdrawals-optional-off-by-default) | off |
| `lightning.backend` | `"fake"` (development) or `"phoenixd"` | required |
| `lightning.phoenixd_url` | phoenixd HTTP API | `"http://127.0.0.1:9740"` |
| `lightning.phoenixd_password` | phoenixd *limited-access* password. Better set via `PHOENIXD_PASSWORD` | — |

Unknown keys are rejected, so typos are caught at start-up.

### Path matching

A protected page must not be reachable under another spelling, whatever
web server sits behind Spikes402. So:

- Request targets longer than 8 KiB get `414 URI Too Long`, and request
  headers are capped at 32 KiB.
- Paths that servers read differently are refused with `400 Bad Request`:
  dot segments and double slashes (`/a/../b`, `//b`), encoded slashes
  and backslashes (`%2F`, `%5C`, `\`), control characters (`%00`,
  tabs, line breaks), and a `#` or `?` in the decoded path (a raw `#`,
  which browsers never send, `%23` or `%3F`), where an origin that
  decodes and re-parses the target could cut the path there, on any path
  that is protected or has a price rule, or is on or near a login route,
  before or after the cut: `/login%23x` could be served as `/login`, and
  `/premium/report.pdf%23.jpg` priced as a `.jpg` but served as the PDF.
  Free paths keep their `#` and `?` (`/wiki/C%23` is served). A `%25` is refused only when a second decode would
  change whether the path is protected (so `/x/%252e%252e/premium/article`
  is still a 400; a harmless `%25` on a free page is not).
- `protect` and `prices` patterns must not end with a trailing slash
  (except `/` itself). `/docs/` never matches a request, because trailing
  slashes are stripped before matching. Use `/docs` or `/docs/**`.
  Patterns that cannot match after this normalisation are also refused.
- Patterns are matched ignoring letter case (unless `protect_case_sensitive`),
  `;parameters` in a segment (`/premium;x/a`, `/x/..;/premium`), trailing dots
  and spaces in a segment (`/premium./a`) and a trailing slash. All of these
  are asked to pay; the request is forwarded unchanged.
- `prices` uses this same matching. Example: articles cost 5 sats, premium
  20, a narrower premium folder 1 (listed first so it wins), everything
  else that is protected uses `price_sats`:

  ```json
  "protect": ["/articles/**", "/premium/**"],
  "price_sats": 5,
  "prices": [
    {"pattern": "/premium/special/**", "price_sats": 1},
    {"pattern": "/premium/**", "price_sats": 20}
  ]
  ```

  The signed L402 token records the price it was issued for, and is bound to
  the exact method and URL. If you raise a price, an invoice that was already
  paid is still honoured at the old price until its token expires
  (`challenge_ttl_seconds`). A proof-of-work token is not: its difficulty was
  set for the old price, so it must cover the new one.

## Dynamic pricing (optional, off by default)

When `surge` is enabled, the price of a **rule group** (a `prices`
pattern if one matches, otherwise the matching `protect` pattern — never
a raw URL) can rise above its base under unusually high 402 demand, then
step back to 1× when traffic returns to normal. The effective price is
`ceil(base × multiplier)` whole sats; proof-of-work uses that same
price. The HMAC token still records the quoted price.

Each group keeps a short-term 402 rate (half-life **3 minutes**) and a
slow baseline of the same rate (half-life **24 hours**). The multiplier
steps up when the short rate is at least `surge_ratio` times the
baseline **and** at least `min_per_minute`, for one
`step_interval_minutes`, up to `max_multiplier` (hard cap 10). It steps
down by the same `step` when that is no longer true. A short spike
barely moves the short rate; a site with steady traffic, even a busy
one, does not surge, because the two rates stay in line.

Until a group has been observed for **one hour**, demand will not raise
the price (the baseline is still being seeded from a simple average).
After a restart the warm-up runs again; baselines are not written to
disk. Global pressure (pending invoices or the Argon2id budget at or
above 90%, or the node breaker open) can still step the multiplier up
the same way. The slow baseline keeps updating during a surge, so a new
normal is accepted over a day rather than frozen.

Defaults when surge is on and a field is omitted: `max_multiplier` 3,
`surge_ratio` 2, `min_per_minute` 60, `step` 0.25,
`step_interval_minutes` 2.

```json
"surge": {
  "enabled": true,
  "max_multiplier": 3,
  "surge_ratio": 2,
  "min_per_minute": 60,
  "groups": { "/articles/**": { "enabled": false } }
}
```

`groups` keys must be a `protect` or `prices` pattern. A group may opt
in or out with `enabled`; omitted knobs inherit the site-wide values.
The live short rate, baseline, ratio and multiplier appear in the
dashboard and `GET /api/status` (`gate.surge`), read-only. Surge
settings stay in the config file.

We count every 402, not unique visitors. A competitor can try to raise a
group's price; `min_per_minute` (default 60, above one IP's usual 402
budget), the ratio against that group's own baseline, the cap and the
step interval limit how far that goes.

## Proof-of-work pricing

The puzzle should cost a bot something comparable to the price, so the
difficulty is derived from it (from this request's price, including a
matching `prices` rule, not only from the default `price_sats`):

```
difficulty = round(log2(price_sats × pow_seconds_per_sat × 2,000,000))   clamped to 16…32 bits
```

If you set `pow_difficulty` yourself (not 0), every path uses that fixed
difficulty, whatever the sats price.

**Assumption:** one commodity CPU core does about **2 million** puzzle
attempts per second (we measured ~2.7 million on a recent server core;
the figure is rounded down). A gaming GPU is roughly **1000×** faster. The
402 answer tells bots the expected time as `expected_seconds_one_cpu_core`.
An optimised native solver (for example in Rust) is about **1.5×** faster
than this Go reference for sha256d and about **1.3×** for argon2id, in an
external benchmark; expected times for those solvers are shorter. The
published calibration is unchanged.

With the default `pow_seconds_per_sat: 10` (used when the key is omitted):

| Price | Difficulty | One CPU core (average) | GPU (roughly) |
|---|---|---|---|
| 1 sat | 24 bits | ~8 s | ~0.01 s |
| 10 sats | 28 bits | ~2 min | ~0.1 s |
| 100 sats | 31 bits | ~18 min | ~1 s |
| 1000 sats | 32 bits (max) | ~36 min | ~2 s |

The example configs use `price_sats: 2` and `pow_seconds_per_sat: 5`: the same 24-bit puzzle as 1 sat at the default 10 seconds per sat (~8 s on one core). Existing sites that omit `pow_seconds_per_sat` keep the default of 10, so their wait time does not change.

Why 10 seconds per sat: a rented CPU core costs about $0.02 per hour, and at
$100,000 per bitcoin one sat is $0.001, the price of about **3 minutes** of
that core. Exact cost parity (`pow_seconds_per_sat: 180`) would make the puzzle
unusable for CPU-only bots, so the default is about 20× cheaper than paying,
while still making mass scraping slow. Raise it if you want work to cost as
much as paying. Specialised hardware will always be relatively cheaper: GPUs
are far faster, and that is accepted. Bitcoin mining ASICs, as far as we
know, are built for 80-byte block headers and cannot directly solve this
puzzle. The 32-bit cap keeps prices above ~1000 sats from producing puzzles
no CPU bot would attempt; set `pow_difficulty` explicitly to go beyond it.

### Argon2id (memory-hard proof-of-work)

With `"pow_algorithm": "argon2id"` the puzzle is
`Argon2id(token + ":" + nonce, salt "spikes402-pow-v1", 1 pass, argon2_memory_mib MiB, 1 lane, 32 bytes)`
with `difficulty` leading zero bits. Each attempt has to fill and read that
much memory, so a GPU or ASIC gains much less over an ordinary CPU than with
sha256d (commonly a single-digit factor per dollar instead of ~1000×; we
have not measured it ourselves). The default stays sha256d because each
check is cheap for the server.

**Pricing.** Same idea as above, with a different speed: one CPU core
processes about **1,500 MiB of Argon2id per second** (we measured ~1,900
MiB/s at 16–64 MiB, rounded down), so one 16 MiB attempt takes ~11 ms:

```
difficulty = round(log2(price_sats × pow_seconds_per_sat ÷ (argon2_memory_mib ÷ 1500)))   clamped to 1…20 bits
```

| Price (`pow_seconds_per_sat: 10`) | 16 MiB | 64 MiB | One CPU core (average) |
|---|---|---|---|
| 1 sat | 10 bits | 8 bits | ~11 s |
| 10 sats | 13 bits | 11 bits | ~1.5 min |
| 100 sats | 17 bits | 15 bits | ~23 min |

More memory means fewer, slower attempts for the same price: it favours
machines with lots of RAM per core less than a smaller setting does, but
costs the server more per check.

**Protecting the server.** Checking a solution costs the server one attempt
(16 MiB and ~10 ms by default), so Spikes402 only runs Argon2id for a
token it signed itself, for this page, not expired and not used yet (all
cheap checks done first). Each check also counts against the client's
`challenges_per_minute`. Concurrent checks share a memory budget
(`argon2_max_memory_mib`, default 256 MiB): at the default 16 MiB per
check that is 16 at once; at 256 MiB per check, only one. A config where
one check needs more than the budget is refused. If the budget is full,
the caller waits briefly and then gets `503` + `Retry-After: 1`, and the
token stays valid. A wrong answer **burns the token** (one check per token,
ever). The parameters are inside the signed token, so a client cannot pick
cheaper ones.

**Future option:** [RandomX](https://github.com/tevador/RandomX) (Monero's
CPU-oriented PoW) narrows the gap to specialised hardware even further, but
it needs ~256 MiB per verifier and a C library. Not implemented.

## Login protection

List your login endpoints in `login_routes` and every login attempt costs
work, always, for everyone. Each failed attempt makes the next one cost
more. Failures alone never block an account; they only make attempts more
expensive. But a real user's price can be raised by others:

- by an attacker anywhere, by up to +4 levels (the account term);
- by anyone sharing the user's /24 or IPv6 /48 (an office, a carrier NAT),
  by up to +10 when they fail on the same account (the pair term) and +6
  when they fail on any account (the network term), or +1 by sending
  proofs that skip the Argon2id work;
- by everyone, by +1 or +2 while the whole site sees a surge of failures.

Load can slow or stop a real user too: while the login Argon2id budget, or
the user's network's share of it, is full, logins get `503`; and the rate
limits below (`429`) apply to everyone behind the same address or prefix.

```json
"login_routes": [{
  "method": "POST",
  "path": "/login",
  "username_field": "email",
  "success": { "status": [302, 303], "location_not_prefix": "/login", "set_cookie": "session_auth" }
}]
```

**How the proxy checks a login**

- **Matching.** `path` is matched in the same canonical form as `protect`
  (see [Path matching](#path-matching)), but always without letter case,
  whatever `protect_case_sensitive` says: a trailing slash, `;params`,
  trailing dots and spaces and case are ignored, so `/Login;jsessionid=1`
  is the `/login` route. `method` defaults to `POST` and is compared
  without letter case. `"/"` cannot be a login path.
- **Near paths and other methods.** Some frameworks serve a login under
  more paths: a format suffix (`/login.json`, `/login.do`) or a path below
  it (`/login/x`). On the login path and on those, only a `GET`, `HEAD` or
  `OPTIONS` that cannot carry a login passes to the origin unchecked: no
  body, no method-override header (`X-HTTP-Method-Override` and the like),
  and no `username_field` in the query string (PHP's `$_REQUEST` and
  Flask's `request.values` read it there). Everything else that is not the
  configured route gets `405`. So a `POST` to an endpoint below a login
  path (for example `/login/2fa`) is refused: move it elsewhere. A link
  that pre-fills the username in the query (`/login?email=...`) gets
  `405` too.
- **Success and failure.** An answer is a success only if all of these
  hold; anything else, including an answer the origin never wrote, is a
  failure:
  - its status is in `success.status`;
  - it does not redirect under `location_not_prefix`;
  - if `location_prefix` is set, it redirects under it;
  - if `set_cookie` is set, it sets that cookie (non-empty, not expired);
  - if `set_cookie` is not set, it does not redirect to the `Referer`
    (which the client chooses, and where origins send failed logins
    "back");
  - if `location_prefix` is set (and `set_cookie` is not), a redirect is
    a success only if the request carried exactly one `Referer`: an
    absolute `http`/`https` URL with a host. No `Referer`, an empty one,
    a relative one, or several count as a failure, because without it
    the origin may send a failure "back" to a page it remembers, which
    the client can put under the prefix;
  - if neither `set_cookie` nor `location_prefix` is set, it does not
    redirect to any query or body value either (a `next` parameter, say).

  If `success.status` lists any `3xx`, the proxy refuses to start unless
  `set_cookie` or `location_prefix` is set: a redirect alone cannot tell
  a success from a failure, and the guess above would count honest logins
  that follow `next` as failures. With `location_prefix` the `next` guess
  is skipped but the `Referer` checks stay; with `set_cookie` all are
  skipped.

  `location_prefix` is unsafe if the origin can send a failed login to
  the previous page by any other means than the `Referer` header while a
  `Referer` is present: an attacker makes that page one under the prefix,
  and every failure counts as a success. Use `set_cookie` for such an
  origin, with a cookie the origin sets **only** on a successful login. A
  session cookie set on every response (`laravel_session`, `PHPSESSID`,
  `connect.sid`, ...) is the wrong choice: failures set it too.

  A login page served with `Referrer-Policy: no-referrer` (or a
  `<meta name="referrer" content="no-referrer">`, or a form with
  `rel="noreferrer"`) makes browsers send no `Referer`: with
  `location_prefix` every login would count as a failure and honest users
  would pay more and more, so such a site must use `set_cookie`. The
  browser default (`strict-origin-when-cross-origin`) sends a `Referer`
  on a same-origin form post. API clients of a `location_prefix` route
  must send a `Referer` too.

  Laravel: a failed login goes to `url()->previous()`, which is the
  `Referer` or, without one, the last page the session visited (an
  attacker sets it with one `GET` under the prefix). The proxy covers
  both: a redirect to the `Referer` is a failure, and so is a redirect
  without a `Referer`. As defence in depth, also send failed logins to the
  login page instead of "back", for example
  `throw ValidationException::withMessages([...])->redirectTo(route('login'))`
  in your `LoginRequest::authenticate()`:

  ```json
  "success": { "status": [302], "location_prefix": "/dashboard", "location_not_prefix": "/login" }
  ```

  A relative `Location` is resolved against the route. Get this right: if
  a failed login can look like a success (for example a site that answers
  `200` both ways and sets the cookie on every answer), every failure
  undoes itself and the price never grows. Pick a cookie the origin sets
  only after a successful login.
- **Username.** The proxy reads at most 16 KiB of an
  `application/x-www-form-urlencoded` or `application/json` body, only to
  find `username_field`. It never stores or logs the body. A request the
  origin could read as another username is refused with `400`: the field
  given more than once under any spelling some framework merges with it
  (NFKC, so full-width `ｕｓｅｒ`; invisible characters dropped; letter
  case under full Unicode case folding plus upper- and lower-casing, so
  `User`, `uſer` with a long s, a Kelvin sign for `k`, or the Turkish
  dotless `ı` and dotted `İ` for `i`; PHP's `user[]`, or `user.name` for
  `user_name`), the field in the query string, a `Content-Encoding`, a
  charset other than UTF-8, or more than one `Content-Type` header. The
  username *value* is counted under one spelling for every spelling an
  origin or its database may treat as the same account: decomposed
  (NFKC/NFKD, so full-width letters and ligatures like `ﬁ` count as plain
  ones), without accents and other combining marks (as accent-insensitive
  collations such as MySQL's `utf8mb4_0900_ai_ci` compare), without
  invisible characters (zero-width spaces, soft hyphens), trimmed, and
  case-folded (`İ` and `ı` as `i`), with letters that do not decompose
  spelled out (`ß` as `ss`, `æ` as `ae`, `œ` as `oe`, `ø` as `o`, `ł` as
  `l`, `đ` and `ð` as `d`, `þ` as `th`, `ħ` as `h`, `ŧ` as `t`). So
  `Élise`, `ELISE` and `ｅｌｉｓｅ` share counters, as do `Søren` and
  `soren`. Other scripts are only case-folded and decomposed; a database
  collation can still merge names this does not. Merging too much only makes two names share
  counters. Plus-addressing (`alice+x@example.com`) and dots in Gmail
  names are **not** merged, because only some sites treat them as one
  mailbox: if yours does, strip them in the origin before the lookup and
  know that an attacker can spread one account's failures over such
  spellings (each one starts its own pair and account counters; the
  network counter still adds them up).
- **Method override.** The login route itself refuses (`400`) any
  method-override header (`X-HTTP-Method-Override`, `X-HTTP-Method`,
  `X-Method-Override`, `X-Original-Method`, `X-Forwarded-Method`) and a
  `_method` field in the query or the body under any spelling the
  username field is matched with (Symfony and Laravel read it), so the
  origin sees the method that was checked. A front proxy must not add
  these headers to every request.
- **Multipart forms are refused.** A login sent as `multipart/form-data`
  (a form with `enctype="multipart/form-data"`), or with any other content
  type, gets `400` and never reaches the origin. This is deliberate, so
  parsing stays small. Make the login form use the default encoding (no
  `enctype`), or send JSON.
- **Redirects to another host.** A `Location` on another host (or
  `//host/...`) is never under `location_prefix`, and without
  `set_cookie` never a success at all. The names in `public_hosts` count
  as this site, so list both `example.com` and `www.example.com` there if
  the origin redirects from one to the other. **Set `public_hosts`
  whenever Spikes402 sits behind another proxy that rewrites `Host`**
  (nginx's `proxy_pass` sends the upstream's name by default): the proxy
  then sees `Host: 127.0.0.1:8402` while the origin redirects to
  `https://example.com/...`, and without `public_hosts` every honest login
  counts as a failure.
- **Forwarding.** The origin gets exactly the body that was checked, with
  its length set and no chunked encoding or trailers.
- **Precedence.** Login routes are checked before `protect`. Neither
  `paused` nor bot policies apply to them.

**Browser login forms**

Add one tag to the login page. `data-field` is the `name` of the username
input:

```html
<script src="/.spikes402/login.js" data-field="email" defer></script>
```

1. When the username field loses focus, the script asks
   `POST /.spikes402/login-challenge` for a challenge bound to that account
   and to where the form submits (the form's `action` and `method`, or the
   clicked button's `formaction` and `formmethod`; no method means `GET`,
   as in HTML, and a `GET` form is not a login route).
2. It solves the challenge in Web Workers (`/.spikes402/argon2id-worker.js`
   and `/.spikes402/pow-worker.js` for the pre-check) while the password is
   typed.
3. On submit it trades the solution at `POST /.spikes402/login-redeem` for a
   one-use cookie, valid for 60 s, and submits the form as usual. Password
   managers keep working.
4. If the page has a Content-Security-Policy, it must allow `script-src`,
   `worker-src` and `connect-src` from `'self'`.
5. `data-text-working` and `data-text-failed` (which may contain
   `{error}`) replace the status messages, for example in another language.
6. `login.js` only handles plain HTML forms. A single-page app that logs
   in with `fetch` or XHR must use the API flow below instead.

**API clients and bots**

A login request without a proof gets `402` with a JSON challenge and a
`WWW-Authenticate: PoW ...` header. Solve two puzzles on the same
challenge (for example with `pow.Params.Solve`): the Argon2id one
(`algorithm`, `difficulty`, `memory_mib`) for `nonce`, and a sha256d
pre-check (`precheck_algorithm`, `precheck_difficulty`) for `pre`. Then
repeat the request with `Authorization: PoW <challenge>:<nonce>:<pre>`.

**Cost to the server**

Checking a proof costs the server one Argon2id hash, so it only does so
after cheap checks pass: the signature and binding of the challenge, that
its level is still enough (a proof below the current level is refused
before anything else), that it is unspent, and the sha256d pre-check. The pre-check is 18 bits: about
2^18 hashes for the client (~0.15 s in a browser worker, more than the
server's Argon2id check in native code) and one for the server. A
challenge is checked with Argon2id at most twice: after a wrong nonce,
once more, no sooner than 2 minutes later; then it stays spent until it
expires. Only its holder, from its network, can send it, so nobody else
can use up an honest user's retry. Login tokens may use at most 250,000
of the 1,000,000 places in the spent-token list, so a login flood cannot
refuse payers ("server busy"). Of those, at most 100,000 hold tokens
checked with a wrong nonce; past that one of them is forgotten, which can
only give that junk token its checks back (still rate-limited), never
replay a right proof. Then:

- **Rate limits** on login challenges and checks together: per client
  (`challenges_per_minute`, per IPv4 address or IPv6 /64), per IPv4 /24
  or IPv6 /48 (four times that) and per IPv4 /16 or IPv6 /40 (32 times
  that). A browser login takes a challenge and a check, so with the
  default 30 that is about 15 logins a minute per address, 60 per /24 and
  480 per /16; past that, `429`. A busy office or carrier NAT behind one
  /24 can hit 60.
- **Memory**: login checks have their own Argon2id budget of 128 MiB (8 at
  a time), separate from `argon2_max_memory_mib`, so a flood of logins and
  the API puzzle cannot starve each other. One /24 or /48 gets at most 2
  of the 8 at a time.
- **Pushback**: for a minute after the budget was full, new challenges ask
  for a 22-bit pre-check (16 times the work). A proof with a right
  pre-check and a wrong Argon2id nonce counts against its network on a
  separate counter that adds at most +1 level (junk is cheaper to send
  than a real failed login), and such proofs and wrong pre-checks count
  against the rate limits.

The pre-check is plain sha256d: an attacker with GPUs or ASICs pays it
almost nothing, and is then held back only by the rate limits, the
per-network slots and the budget.

**Fail-closed**

The proxy enforces everything, so a missing or broken script tag breaks the
login, never the wall. A login request that cannot be checked never reaches
the origin. That covers:

- a body that is too large;
- an unknown content type, no username, or an ambiguous one (see above);
- a method other than the route's, or a near path (see above);
- a method-override header or `_method` field;
- a wrong, reused, expired or underpriced proof;
- a missing or wrong pre-check;
- a full login Argon2id budget, or a network's share of it (`503`).

**Escalation**

The level is counted in sha256d-equivalent bits. The puzzle is Argon2id at
16 MiB with *level − 16* bits, and each level doubles the work.

```
level = 23 + max(pair, account, min(network, 6)) + surge, at most 33
```

- `pair` counts failures for this account from this network (a /24 for
  IPv4, a /48 for IPv6).
- `account` counts failures for this account from anywhere. It adds
  nothing below 4 failures, then 4 at once (see below). The cap stops an
  attacker from making a real user's login expensive.
- `network` counts failures from this network on any account.
- Failure counts map to extra levels as 0, 1, 2, 3, 4, 6, 8, 10 for
  0, 1, 2 … 7+ failures.
- `surge` adds 1 for everyone while the whole site sees 100 or more failed
  logins a minute, and 2 at 1,000. Set your own thresholds with
  `"login_surge_failures_per_minute": [100, 1000]`. The two counts must
  increase, and the first must be at least 1. Pick values well above your
  normal failure rate.
- Every attempt counts as a failure until the origin answers success, so
  parallel attempts cannot share one level.
- After a success, the pair counter starts again from zero. The account
  and network counters lose only that attempt and otherwise decay, so a
  real user's login does not show to others (through the account term)
  and does not wipe a network's failures.
- Counters halve every 30 minutes (pair and account) and every 15 minutes
  (network).

| Failures | Level | Argon2id bits | One native core | Browser, 4 workers |
|---:|---:|---:|---:|---:|
| 0 | 23 | 7 | 1.4 s | 2.8 s |
| 1 | 24 | 8 | 2.8 s | 5.5 s |
| 2 | 25 | 9 | 5.6 s | 11 s |
| 3 | 26 | 10 | 11 s | 22 s |
| 4 | 27 | 11 | 23 s | 44 s |
| 5 | 29 | 13 | 90 s | 3 min |
| 6 | 31 | 15 | 6 min | 12 min |
| 7+ | 33 | 17 | 24 min | 47 min |

The native core figure is the measured ~11 ms per 16 MiB attempt. The
browser figure assumes ~86 ms per attempt per worker, measured with the
JavaScript solver in V8. Phones are slower; more cores are faster. All
times are averages.

A challenge stays valid for 8 times the browser average of its level,
at least 2 minutes and at most 6 hours (2 minutes up to level 25, then
about 3, 6, 24 and 94 minutes at levels 26, 27, 29 and 31, and 6 hours
at 33),
so a raised level makes a login slow, never impossible because the
challenge expired first. A phone about four times slower than the table
still gets twice its average. A long life opens no stockpile: a proof is
bound to its account, network and level, and once one attempt is made
the level is higher and the other proofs are refused. What can still
cost an honest browser its work is the level rising while it solves (a
new failure on the same account from its network, the account's +4, or
a surge starting): the proof is refused and the script must start over
at the new level, which stops at 33.

**What a challenge reveals**

Anyone can ask for a challenge for any username, and the level is in the
answer, so it reveals a little:

- about the account, one bit: whether it had about 4 or more recent
  failures from anywhere (the account term is 0 or +4, nothing between).
  A successful login does not clear that bit, so it does not show when
  the real user logs in;
- about the asker's own network: its recent failures, on that account and
  overall, which people sharing the network (an office, a carrier NAT)
  can see;
- the site-wide surge, and whether the login budget was recently full
  (the pre-check difficulty).

It never reveals whether an account exists.

**What is stored**

- The counters live in memory, at most 1,000,000 entries.
- Their keys are HMACs (with `secret_key`) of the account name and the
  network. No username, password or IP address is kept.
- When the table is full, the tenth of the counters that weigh least now
  (fewest failures, decayed most) are forgotten to make room. Finding
  them scans the whole table under its lock: about 70 ms at 1,000,000
  entries, once per 100,000 new counters, during which logins wait. The counters
  of an attack in progress weigh most and stay; newcomers start at the
  base level.
- With a configured `secret_key`, the counters are saved every minute to
  `state_dir/login-counters`, sealed with an HMAC, so a restart does not
  forget an attack. A file that fails its seal stops the proxy at startup
  rather than being ignored.

## The 402 page (for people)

A browser (a request that accepts `text/html`) gets a page instead of JSON:
"This page is protected by an invisible energy wall built with Bitcoin. Pay a
few sats with the Spikes402 extension, or solve a math puzzle with your
computer's power." It shows the Lightning invoice (QR code, an **Open in wallet** link for a
phone or desktop wallet, and a one-click button if a WebLN wallet is present)
and a **Solve the puzzle** button. After you pay with any wallet the page
shows "Waiting for payment…", then "Paid — unlocking", and reloads into the
content. It learns that the invoice is paid by polling `/.spikes402/claim`
(see below); the preimage never has to be typed in.

With `"wall_mode": "pow_first"` (off unless you set it; used in
[`config.example.json`](config.example.json)) the puzzle starts on its own, and
Lightning sits under the line “Tired of waiting? Pay this →”. The invoice, QR
code, **Open in wallet** link and WebLN button are the same as on the default
page, and are still created when the page is served. If the visitor has
`prefers-reduced-motion: reduce`, the puzzle does not start until they press
the button.

The button runs the same sha256d puzzle as bots do, in Web Workers (one per
core, at most 8), and shows the work: a progress bar against the expected
number of attempts, attempts so far, hashes per second, elapsed time. When
a worker finds the nonce, the page redeems it exactly like a bot would
(`Authorization: PoW token:nonce`) and reloads into the content. Nothing is
loaded from outside; the page's Content-Security-Policy allows only hashed
inline scripts, `connect-src 'self'` (for the claim polls and the redeem
request) and workers from the proxy itself (`worker-src 'self'`). The
worker and the claim endpoint live under `/.spikes402/`; that prefix is
reserved and never reaches your website. With `argon2id` the button is
hidden (the browser solver is sha256d only); bots and the extension work as before.

**Difficulty.** The page uses the same difficulty as everyone. A lower
"interactive" difficulty would only be safe if the server could tell people
from bots, and it cannot (`Accept: text/html` costs a bot nothing), so it
would just be a cheaper price for scrapers. The knob stays `pow_seconds_per_sat`.
The in-browser solver does about 1 million attempts per second per thread
(about half of the Go reference core) and scales with cores: 8 threads
measured ~7–10 million per second in Chrome. So difficulty 24 (1 sat at the default 10 seconds per sat, or 2 sats at 5)
takes a few seconds on a laptop, 26 about 10 s, and 28 (10 sats at the default
10 seconds per sat) roughly a minute. If you want visitors to wait about 10 s, price pages so
the difficulty is 25–26 or set `pow_difficulty`.

**Teaser.** To tempt visitors (and search engines) you can show a few words of
the page itself. It is off by default; turn it on per path pattern:

```json
"previews": [
  { "pattern": "/premium/free-sample/**", "mode": "none" },
  { "pattern": "/premium/**", "mode": "excerpt", "max_chars": 300 }
]
```

The proxy fetches the page from your website as an anonymous visitor (GET,
path only, no cookies, no credentials), reads the title, the meta
description and, in `excerpt` mode, the first paragraphs, and shows that on
the 402 page and in a `preview` field of the JSON answer. Everybody sees the
same teaser, bots included. The text is plain: markup, scripts, control and
invisible characters and angle brackets are removed, and it is escaped again
on output. It is capped (title 150, description 300, excerpt `max_chars`) and cut
at a word. Your website is protected from the feature: only a 200 `text/html` answer
counts, at most 256 KiB are read, a fetch times out after 3 s, results are
cached for 5 minutes (failures for 1 minute, 256 pages at most), at most 2
fetches run at once and 60 per minute in total; beyond that the page has no teaser.
Only the paths and the length you configure are ever shown for free.
The page also carries schema.org `isAccessibleForFree: false` markup for the
`.paywall` block, as Google recommends for paywalled content.

**Search engines.** The 402 answer is not indexed. Google does not index URLs
that answer with a 4xx status, and drops those already indexed, ignoring
their content ([Google's documentation](https://developers.google.com/search/docs/crawling-indexing/http-network-errors));
so the teaser and the markup help people and AI agents, not Google
search. Spikes402 keeps `402` on purpose, because clients depend on it. If you want
Google to index a page, the existing options are: a bot policy `free` for
verified Googlebot (Google's own paywall model, which is not cloaking when the
page carries the paywall markup), or leaving that page unprotected. Serving the teaser
with status 200 to everyone would be indexable, but changes what a 402 means
for bots; it is not implemented.

## Bot identification

On protected paths Spikes402 tries to tell who is asking, logs it (at most
60 lines a minute) and tells the client in a response header:

```
Spikes402-Class: verified-signed; name="https://bot.example/.well-known/http-message-signatures-directory"; method=web-bot-auth
Spikes402-Class: verified; name="Googlebot"; method=ip-list
Spikes402-Class: impostor; name="GPTBot"; method=ip-list
Spikes402-Class: unknown
```

| Class | Meaning |
|---|---|
| `verified-signed` | The request carries a valid **Web Bot Auth** signature (below). Checked first |
| `verified` | The User-Agent claims a known crawler and the address checks out against the operator's published IP list (or, if the address is not in the list or no list is loaded yet, forward-confirmed reverse DNS) |
| `impostor` | The User-Agent claims a known crawler but the address definitely does not belong to it |
| `unknown` | Everything else, including claims that could not be checked (DNS timeout, list not loaded); a `note` says why |

Built-in crawlers (IP lists refreshed daily, kept on failure): Googlebot,
Bingbot, GPTBot, OAI-SearchBot, ChatGPT-User, ClaudeBot, PerplexityBot,
Perplexity-User, Applebot, DuckDuckBot, CCBot. Replace the list with
`known_bots`. Reverse DNS has a 1.5 s timeout, at most 16 lookups at once
and a cache of 10,000 addresses (positive and negative). For Googlebot, a
PTR name must end in `googlebot.com` or `google.com` (not `googleusercontent.com`),
and a forward lookup of that name must return the same address.

### Web Bot Auth

[Web Bot Auth](https://datatracker.ietf.org/wg/webbotauth/about/) lets a
bot sign each request with [HTTP Message Signatures (RFC 9421)](https://www.rfc-editor.org/rfc/rfc9421)
and publish its public keys at
`https://<its domain>/.well-known/http-message-signatures-directory`. The
request names that URL in a `Signature-Agent` header. Spikes402 follows
[draft-ietf-webbotauth-httpsig-protocol](https://datatracker.ietf.org/doc/draft-ietf-webbotauth-httpsig-protocol/)
and checks the draft's own test vectors. Ed25519 only, using Go's standard
`crypto/ed25519`.

Where the drafts leave room, Spikes402 takes the **strict** reading:

- The signature must cover `@authority` (or `@target-uri`) and the
  `signature-agent` header member, carry `created`, `expires`, `keyid` (the
  key's JWK thumbprint) and `tag="web-bot-auth"`, and be valid for at most
  **1 hour** (the draft allows 24 h); 1 minute of clock skew is tolerated.
- Only these components can be signed: `@method`, `@authority`, `@scheme`,
  `@target-uri`, `@request-target`, `@path`, `@query`, plain headers and
  `signature-agent`. Anything else makes the signature "unverified".
- **Replay protection:** a signature (its nonce, or the signature itself) is
  accepted once. The memory for this is capped at 100,000 entries; when full,
  signatures count as unverified rather than risk a replay.
- Signatures only count over **https** (directly or via a trusted proxy that
  sets `X-Forwarded-Proto: https`).
- **Key directories** are fetched only from `https://` on port 443, never
  from private, loopback or link-local addresses (no SSRF), without
  redirects, in at most 5 s and 64 KiB, with at most 32 keys, and the media
  type must be `application/http-message-signatures-directory+json`. They are
  cached per `Cache-Control` (5 min – 24 h; failures for 1 min). If a refresh
  fails, the keys already known stay valid for up to 24 h. At most 1,000
  directories are cached and 30 new fetches a minute are made.
- The draft's **published test key is refused**, as the draft asks.
- The first request from a new bot may wait up to 1.5 s for its directory;
  if it is not there in time the request counts as "unverified".
- A bad or unverifiable signature does not block anything: the request is
  classified by the other checks, with a note.
- **Anyone can publish a key**, so a valid signature alone proves nothing
  about *who* signed. A request is only `verified-signed` if the key
  directory is on a domain listed in `web_bot_auth_trusted_agents` (or a
  subdomain of one). Other valid signatures are noted and the request is
  classified by the other checks. With the default empty list, nobody is
  `verified-signed`.
- **No relaying:** with `public_hosts` set, a signature only counts if the
  request's host is one of your host names, so a signature a bot made for
  another site cannot be replayed against yours.

### Policies: off by default, open for discussion

Should verified or signed bots pay less, more, or nothing at all? We do
not know yet. So **by default every client pays the same price, verified or
not** (no free pass, multiplier 1); identification is used for logs,
transparency and spotting impostors.

For those who want to experiment, `bot_policies` maps a class (`unknown`,
`impostor`, `verified`, `verified-signed`), a bot name (`"GPTBot"`) or a Web
Bot Auth directory URL to an action:

```json
"bot_policies": {
  "impostor": {"action": "pow-only"},
  "verified": {"action": "pay", "price_multiplier": 2}
}
```

Actions are `pay` (normal), `pow-only` (no invoice), `free` and `block`
(403). `price_multiplier` scales the price and the puzzle (minimum 1 sat).
Bot names and URLs only match once verified, so an impostor never gets a
named bot's treatment, and `verified-signed` only covers agents on your
`web_bot_auth_trusted_agents` domains. Note that a bot can always drop its claim and be
`unknown`, so a policy for `unknown` is the effective floor.

## Protecting your Lightning node

The paywall must never become a way to flood your node with invoice
requests. Every unpaid request gets a proof-of-work challenge, which costs
the server almost nothing. An invoice is only added when *all* of these
allow it; otherwise the 402 answer offers proof-of-work only and the node is
not called:

| Guard | Default | What it stops |
|---|---|---|
| Per-client limit: `challenges_per_minute` per IP (per /64 for IPv6) | 30 | One client hammering the paywall. Beyond it: `429 Too Many Requests` + `Retry-After`. Free pages are never limited |
| Same offer reused for the same client and page while more than half its lifetime is left. "Same client" means same IP **and** same `spikes402_session` cookie (see below) | on | Reloads creating new invoices |
| Global rate: `max_invoices_per_minute`, spread evenly (bursts up to 5 seconds' worth) | 300 | Many clients at once (botnets) |
| Global cap: `max_pending_invoices` open at once | 10,000 | Unpaid invoices piling up |
| At most 8 invoice requests in flight, each with a 3-second timeout | on | A slow node tying up the proxy |
| Circuit breaker: after 5 failures in a row, the node is left alone for 30 s; after that a single failure pauses it again | on | Hitting a node that is already struggling |

A visitor hanging up mid-request does not count as a node failure, so
nobody can trip the breaker on purpose. Failures are logged at most once a
minute.

**Session cookie.** 402 answers set an optional cookie, `spikes402_session`
(random value, `HttpOnly`, `SameSite=Lax`, `Secure` over https, 24 h). A
client that sends it back gets its open offer again instead of a new
invoice. A client without it (most bots) gets a fresh offer on each
request, still counted by every guard above, so the node is not called more.
Two clients behind the same IP (office, carrier NAT) therefore never share
an offer, and nobody can use up an invoice someone else is paying. The cookie
is removed before the request goes to your website. Even so, a paid invoice
can always be used once by whoever paid it: if the offer's puzzle was solved
first, the payment still unlocks the page once (work after a payment is refused).

**Real client IPs.** Behind a reverse proxy or CDN, every request seems to
come from the proxy. Set `client_ip_header` *and* `trusted_proxies`: the
header is only believed on connections from those addresses. It is read
from right to left, skipping trusted proxies, so a fake address a client
puts at the start of `X-Forwarded-For` is ignored. The same rule applies
when forwarding to your website: `X-Forwarded-*` and the client-IP header
are kept or appended only from a trusted proxy; any other peer's spoofed
headers are stripped and replaced with the connection address. By default
the `Host` sent to the origin is the host of `origin`. Set `preserve_host`
if your website is a name-based virtual host and must see the public host
name; that only applies to trusted proxies, and if `public_hosts` is set,
only those names are forwarded.

**Bounded memory.** Rate-limit windows, open offers and used tokens are
evicted when they expire. Hard caps also apply: 100,000 tracked clients per
minute (newcomers beyond that share an allowance per IPv4 /16 or IPv6
/32, 8 times a client's, for up to 10,000 such prefixes, then one
common allowance; so filling the table from many IPv6 /64s throttles
newcomers near the attacker, not everyone), `max_pending_invoices`
open invoices (counted until they expire, even if the visitor later pays
with proof-of-work), and 1,000,000 used tokens. When the used-token list
is full, new redemptions are refused ("server busy") rather than risking a
replay.

## Admin dashboard

Off by default. Set an admin key and restart:

```sh
export SPIKES402_ADMIN_KEY=$(openssl rand -hex 32)
bin/spikes402 -config config.json
# then open http://127.0.0.1:8404 and paste the key
```

The dashboard listens on its **own address, localhost only by default**,
never on the public proxy port. From another computer use an SSH tunnel
(`ssh -L 8404:127.0.0.1:8404 your-server`), or set `admin.listen` plus
`admin.allowed_ips` (only the TCP address counts, never forwarded headers).
The key is compared in constant time, kept only in the browser tab
(`sessionStorage`) and sent as `Authorization: Bearer`, so there are no cookies
to steal or forge. The page is plain HTML/JS embedded in the binary, with a
strict Content-Security-Policy and no outside resources.

It is **one page**: the siege monitor (below), four numbers and a small
Lightning light (green = fine, red = a problem; hover it for details).
Everything else sits behind the discreet **Settings** link:

- the few **webmaster settings** that can change while running: `price_sats`,
  `prices` (per-path rules; first match wins), `protect`, `pow_seconds_per_sat`,
  `challenges_per_minute`, `bot_policies` (the four visitor classes as
  dropdowns) and `paused`. They are validated like the config file, apply
  immediately, and are saved under `state_dir` as `config.overrides.json`.
  `config.json` itself is never rewritten. The change is validated, then
  the overrides file is written atomically (mode `0600`), and only then
  published to the running proxy: a change that fails validation or cannot
  be saved is not applied. The file contains only those dashboard fields
  (unknown keys, including secrets or Lightning settings, are refused on
  load). On start, overrides win over the same fields in `config.json`,
  and the process logs one line naming the file and which fields changed
  (values are not secrets). A leftover `config.overrides.json` next to
  the config file is still read if the `state_dir` copy is not there yet;
  the next dashboard save writes to `state_dir`. Delete the overrides
  file if you want the config file values back. The last settings a
  person applied are also kept under `state_dir` as
  `config.confirmed.json`, so the AI cannot reach those confirmation
  limits through many small steps; delete that file too if you want that
  baseline reset. Everything about money or
  the perimeter stays in the config file on purpose: passwords and keys,
  the Lightning node, the withdraw destination, node anti-flood limits
  (`max_invoices_per_minute`, `max_pending_invoices`), surge pricing,
  ports, trusted proxies / IP header, and admin allowed IPs. The live short rate, baseline, ratio and multiplier of each surge group are shown in status (read-only).
  A stolen admin key can change
  prices, protection and pause, but cannot drain or flood the node.
  **Pause** serves protected paths free and creates no invoices at all
  (no load on the node). Only the human admin can toggle pause (on or
  off); the AI key may propose it but cannot apply it, even when
  `admin.ai_can_apply` is on. With `ai_can_apply`, the AI may only apply
  changes that make protection stricter or equal (for example a higher
  price). Anything that would reduce protection — removing or narrowing
  `protect` patterns, a `free` bot policy, a lower price, or easier
  proof-of-work — is shown as a suggestion and needs your click, with a
  warning, in the dashboard. So does anything that could take the site
  down even though it looks stricter: a whole-site `protect` pattern
  (`/`, `/**`), making verified crawlers or signed agents pay or be
  blocked, a price more than doubling from the last settings a person
  applied (or above 1000 sats), or cutting `challenges_per_minute` by
  more than half from those same settings. Those proposals are marked in
  the dashboard and the API only applies them with an explicit
  confirmation (`POST /api/proposals/{id}/approve?confirm=1`). A collapsed list shows all current settings
  (no secrets) and node details;
- **AI proposals**: changes suggested by the AI admin, to approve or reject;
- **Withdraw**, in its own red box at the bottom, only if enabled.

### Siege monitor

![Siege monitor](docs/siege-monitor.png)

Every request to a protected page is a dot walking from the left to the
glowing wall in front of your site. Four colours, matching the four numbers:

- **gold**: paid ⚡ (passes the gate into the site)
- **cyan**: did the proof-of-work (passes too)
- **grey**: stopped at the wall, told the price (`402`)
- **red**: repelled (fake proof, too fast `429`, busy `503`, blocked `403`)

On top: visits per minute, paid, proof-of-work and repelled. Hover a dot to
see who it is (`GPTBot`, `Googlebot (fake)`, …); names are written next to
the dots only when few are on screen. "Last 10 visits" opens a small table.

It polls the API once a second; nothing is pushed when nobody watches. The
server keeps only the **last 1,000 visits** in a ring buffer plus counters
(at most 200 bot names; the full breakdown by class, outcome and bot is in
`GET /api/status` and the MCP tools). Visitors are **anonymised by default**:
a keyed hash that changes on every restart (`v-3fa2c1d9e07b`), no query
strings. Use `"admin.visitor_ips": "truncate"` for networks
(`203.0.113.0/24`) or `"full"` for addresses. The animation respects
"reduce motion".

### Public siege view (optional, off by default)

With `"public_siege": true`, anyone can watch a read-only copy of the siege
at `/.spikes402/siege`, and the 402 page shows a “See the siege in real
time” link. It is served by the public proxy itself, not by the admin
listener, and shares nothing with the admin API.

It runs **8 seconds behind** and is **anonymised**: the JSON behind it
(`/.spikes402/siege.json`) holds, per second, only four counts (paid,
proof-of-work, stopped, repelled) and at most six names of crawlers from the
bot list (`GPTBot`, or `Googlebot` marked fake). No addresses, hashes, paths,
query strings or user agents; signed agents and unknown visitors get no
name. The snapshot is rebuilt at most once per second and shared by every
viewer (a few KB at most), so more viewers do not mean more work, and it
never touches the Lightning node. The page has no external resources, a
strict CSP, polls once a second only while the tab is visible, and respects
"reduce motion".

The delay is there so nobody can watch their own requests land in real time.

## AI admin (MCP)

`bin/spikes402-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io)
server (stdio) that lets an AI assistant (Claude Desktop, Cursor, …) watch the
proxy and suggest changes. Give it its **own key**, `admin.ai_key`:

```json
{"mcpServers": {"spikes402": {"command": "/path/to/bin/spikes402-mcp",
  "env": {"SPIKES402_AI_KEY": "<admin.ai_key>", "SPIKES402_ADMIN_URL": "http://127.0.0.1:8404"}}}}
```

Tools: `get_status`, `get_recent_visits`, `get_settings`, `list_proposals`,
`propose_settings_change` and `apply_settings_change`. With the AI key the API
allows reading and **proposing** only; a proposal changes nothing until you
approve it in the dashboard. Applying directly needs `"admin.ai_can_apply":
true` in the config file, and even then only for changes that do not reduce
protection or need extra confirmation (see above); those stay proposals. The AI key can **never** approve
proposals or withdraw; there is no withdraw tool; and `spikes402-mcp`
refuses to run with the human admin key.

## Withdrawals (optional, off by default)

The dashboard can send money from the node to **one fixed destination**
written in the config file. It cannot be changed through the dashboard, the
API or the AI.

```json
"withdraw": {
  "enabled": true,
  "destination": "you@your-wallet.example",
  "feerate_sat_vbyte": 0
}
```

```sh
export SPIKES402_WITHDRAW_PASSWORD='a long passphrase, not the admin key'
export PHOENIXD_FULL_PASSWORD=<http-password from phoenix.conf>
```

- **Destination**: a Lightning address or a
  [BIP353](https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki)
  address (both look like `name@domain`; a leading `₿` is accepted and
  removed). phoenixd's `/paylnaddress` pays both: it tries BIP353 first,
  then the classic Lightning address (LNURL). Or a Bitcoin address
  (on-chain via `/sendtoaddress`, which needs `feerate_sat_vbyte`). A raw
  BOLT12 offer (`lno1…`) is not accepted as a destination (the donation
  offer below is for sending *to* this project, not for withdrawals).
- **Routing fee reserved**: for Lightning destinations the amount plus
  Phoenix's routing fee (4 sats + 0.4 %, rounded up) must fit in the balance;
  if it does not, the error tells you the largest amount you can withdraw.
- **Unknown outcome**: if phoenixd does not answer clearly (for example a
  timeout), the withdrawal is recorded as "unknown — check the node", not as
  failed, and new withdrawals are blocked. Check the node's outgoing payments
  (`phoenix-cli listoutgoingpayments` or the Phoenix logs), then click
  "It was sent" or "It was not sent" on the dashboard (withdraw password
  needed). This prevents paying twice.
- **⚠ phoenixd full-access password.** Withdrawing needs phoenixd's *full*
  `http-password`, which can spend everything on the node. It is only read
  when withdrawals are enabled and is used only by the withdrawal code; the
  paywall itself keeps using the limited, receive-only password. If you do not
  need withdrawals, leave them off and use phoenixd's own tools instead.
- **Two confirmations**: step 1 sends the amount and the withdraw password
  and returns a one-time confirmation valid for 2 minutes, showing the
  destination; step 2 needs the same amount and the password again (plus a
  browser confirm). Each confirmation works once.
- The **withdraw password** is separate from the admin key (≥ 16 characters,
  must differ from every other secret). After 5 wrong passwords withdrawals
  lock for 15 minutes. The admin key alone cannot withdraw.
- **Keep withdrawals off when you are not withdrawing.** While they are on,
  the internet-facing proxy holds a password that can spend everything, and
  it logs a warning at start-up. Safest: withdraw with phoenixd's own tools
  (`phoenix-cli`) and never enable this. See [SECURITY.md](SECURITY.md).
- Only the human admin key can withdraw; the AI key never can.

## Using a real Lightning node: phoenixd

[phoenixd](https://phoenix.acinq.co/server) is a minimal, self-hosted,
non-custodial Lightning node by ACINQ. Your keys stay on your server.

1. Install and start phoenixd on the same machine (see its documentation).
   On first start it writes `~/.phoenix/phoenix.conf` and a seed:
   **back up `~/.phoenix/seed.dat`**.
2. Copy `http-password-limited-access` from `phoenix.conf`. That password can
   create invoices but **cannot spend**, which is all Spikes402 needs.
3. Configure and run:

   ```sh
   export SPIKES402_SECRET_KEY=$(openssl rand -hex 32)
   export PHOENIXD_PASSWORD=<http-password-limited-access>
   # in your config: "lightning": {"backend": "phoenixd"}
   bin/spikes402 -config config.json
   ```

phoenixd gets inbound liquidity automatically; the first payments it
receives pay for opening its channel, so small amounts may not show up in
your balance straight away. Read phoenixd's docs on fees before going live.

To run the proxy and phoenixd with Docker, see [Deploying Spikes402](docs/deploy.md).
Published images are at `ghcr.io/choffolo/spikes402` (linux/amd64 and linux/arm64):

```sh
docker pull ghcr.io/choffolo/spikes402:v0.2.0   # or :latest
docker run --rm -p 127.0.0.1:8402:8402 \
  -v "$PWD/config.json:/etc/spikes402/config.json:ro" \
  -v spikes402-state:/var/lib/spikes402 \
  -e SPIKES402_SECRET_KEY -e PHOENIXD_PASSWORD \
  ghcr.io/choffolo/spikes402:v0.2.0
```

The example [`docker-compose.yml`](docker-compose.yml) builds locally by default.
To use a released image, set `image: ghcr.io/choffolo/spikes402:v0.2.0` (or
`:latest`) on the `spikes402` service and drop `build: .`. How to verify the
image signature is in the deploy guide.


## For bot authors

`Authorization` formats and the JSON body are documented above and in every
402 answer. In Go you can use the [`client`](client) package:

```go
c := &client.Client{Mode: client.ModePoW}
resp, err := c.Get(ctx, "https://example.com/premium/article")
```

Paying needs a `client.Payer` (your wallet). Only a fake payer exists so far.
The client solves both `sha256d` and `argon2id` puzzles and refuses ones
that are too hard (`MaxDifficulty`, `MaxArgonDifficulty`, `MaxArgonMemoryMiB`).

**Waiting for an external payment.** If you pay the invoice with a wallet
that does not give you the preimage in the same process, poll the claim
endpoint advertised as `l402.claim` (always `/.spikes402/claim`):

```
GET /.spikes402/claim
Authorization: Bearer <token>     # the token from the 402 body, no preimage
```

A 202 body `{"status":"unpaid"}` means not yet (honour `Retry-After`, about 2
seconds). A 200 body `{"status":"paid","preimage":"<hex>"}` is the preimage;
redeem the page as usual with `Authorization: L402 <token>:<preimage>`. The
same token is also accepted with `Spikes402-Redeem: cookie` on a POST to the
claim path: the response is 204 and a pass cookie (`pass_ttl_seconds`, default 10) for that
resource, so a browser can reload the page without handling the preimage.
A 410 means the offer expired; a 401 means the token was missing or not
ours. Only the holder of the HMAC-signed token can claim; knowing the
invoice or the QR code is not enough. Polls are rate-limited per client,
and lookups of the same payment hash are coalesced and cached so many
tabs do not become many calls to the Lightning node.


**Signing your requests (Web Bot Auth).** Set `client.Signer` to a
`webbotauth.Signer` with your Ed25519 key and the https URL where you
publish your key directory (`webbotauth.DirectoryHandler` serves it). The
example bot can do it all:

```sh
bin/spikes-bot -sign -key-file bot.key -agent-url https://bot.example/.well-known/http-message-signatures-directory URL
# prints the key directory (JWKS) to publish at that URL

# local test: serve the directory from the bot itself (gate needs "web_bot_auth_dev_mode": true)
bin/spikes-bot -sign -serve-directory 127.0.0.1:8403 http://localhost:8402/premium/article
```

## Security notes

- **Single use, per resource, short-lived.** Tokens are HMAC-SHA256 signed and
  carry the payment hash, a hash of the resource, the expiry and the puzzle difficulty.
  A token cannot be reused, moved to another page, or forged without the
  secret key. Wrong attempts do not burn a valid token, except with Argon2id
  (see above).
- **Not real macaroons.** The `macaroon="…"` field holds this simpler signed
  token. The header layout follows L402, so clients that treat the token as
  an opaque string work; clients that decode macaroons (such as Lightning
  Labs' own tools) will not understand it yet. Caveats/delegation are not
  supported.
- **The secret key** must stay secret and stable: anyone who has it can mint
  tokens. Never commit it; use the environment variable.
- **Your credentials stay with you:** Spikes402 removes its own `Authorization`
  header and session cookie before forwarding to the origin and refuses
  ambiguous paths (see [Path matching](#path-matching)).
- **Dangerous settings are refused** unless `unsafe_allow_dangerous_config`
  is set: trusting every address as a proxy, opening the dashboard to the
  whole internet, or exposing the fake backend (with it, anyone can "pay").
- **Teasers show page text for free.** Only for the patterns and the length you
  set (default: nothing). The fetch is anonymous, path-only, capped and cached; see
  [The 402 page](#the-402-page-for-people).
- **Only receiving:** use phoenixd's limited-access password, never the full one.
- **Proof-of-work is cheaper on special hardware.** The difficulty is
  calibrated for a CPU core (see [Proof-of-work pricing](#proof-of-work-pricing));
  a GPU solves it about 1000× faster.
- **Client IP header:** it is only trusted from `trusted_proxies`; list only
  proxies you control. Spoofed `X-Forwarded-*` and client-IP headers from
  anyone else are stripped before the request reaches your website.
- **Admin dashboard:** keep it on localhost (default) and reach it through an
  SSH tunnel. Over plain http on a network the key could be sniffed; use
  `admin.allowed_ips` only on a network you trust, or put TLS in front.
- **Keys are separate:** admin key, AI key, withdraw password and phoenixd's
  two passwords must all differ; the config check refuses reused ones.

## Known limits / not done yet

- Surge pricing is off until you enable it; baselines start a one-hour
  warm-up after each restart (they are not saved to disk). See
  [Dynamic pricing](#dynamic-pricing-optional-off-by-default).
- If your website fails to answer (a 5xx status or no answer) for a GET or HEAD
  that carried a valid proof, the same proof can be sent again: up to 3 times
  within 30 seconds, and it is delivered at most once. Other methods get no
  second try.
- Spent tokens are remembered in memory (about **128 MiB** at the cap of
  1,000,000 unexpired tokens, ~134 bytes per entry) and, when `secret_key`
  is set, also on disk in `state_dir` so a restart cannot replay them.
  Expired ones are dropped from memory every 5 minutes and from the disk
  snapshot at each compaction.
  Run **one** proxy instance. Without a configured `secret_key` (the fake
  backend's random key), a restart still forgets them.
- Rate limits are per IP. A botnet with many addresses can use up the global
  invoice budget; paying visitors then get proof-of-work only until it
  refills. The node itself stays protected. Bots behind one shared address
  (office, carrier NAT) share a limit (but never an offer).
- More security limits are listed in [SECURITY.md](SECURITY.md).
- The proof-of-work price is an estimate (see above), not a market price.
- If the site itself uses the `Authorization` header on protected paths,
  it will conflict with Spikes402.
- No real wallet in the example bot; no LND backend.
- Bot identification is best effort: IP lists and reverse DNS only show that
  a request came from the operator's network, and anyone can stay
  `unknown`. Web Bot Auth is still an IETF draft; the format may change.
- The Web Bot Auth replay memory is per process and capped (see above).
  Directory response signatures are produced by the example bot but not
  required by the gate (the draft allows that).
- Argon2id speed figures for GPUs are not measured by us.
- Dashboard: one admin key (no user accounts, no audit log beyond the
  process log); webmaster settings persist in `state_dir/config.overrides.json`
  (`config.json` is never rewritten); the siege monitor shows this process
  only and resets on restart.
- Withdrawals: tested against the fake backend and a mock of phoenixd's API,
  not yet against a real phoenixd node. Fee estimation is up to you
  (`feerate_sat_vbyte`); Lightning-address payments may fail if no route is
  found.
- Login protection: proof-of-work only for now. A Lightning alternative
  with the same escalating price, and solver fingerprinting for the siege
  monitor, are planned. The browser solver is plain JavaScript, about 8x
  slower per thread than native Argon2id, so attackers with native code get
  a head start. Multipart login forms are refused (fail-closed, see
  [Login protection](#login-protection)).
- Not yet reviewed by Lightning security experts.

## Development

```sh
go test -race ./...
```

A CycloneDX SBOM and an OpenVEX document are generated in CI; see
[Software bill of materials and VEX](SECURITY.md#software-bill-of-materials-and-vex).

Layout: `cmd/spikes402` (proxy), `cmd/spikes-bot` (example bot),
`cmd/demo-origin` (test website), `internal/gate` (402 logic),
`cmd/spikes402-mcp` (AI admin), `internal/admin` (dashboard, siege monitor,
withdrawals), `internal/mcp`, `internal/lightning` (fake + phoenixd),
`internal/config`, `internal/botid`
(bot classification), `internal/sfv` (structured header parsing), `webbotauth`
(Web Bot Auth sign/verify), `pow`, `client`, `extension/` (browser wallet).

## Browser wallet extension (prototype)

[`extension/`](extension/) is a Manifest V3 wallet (v0.1.6) that injects
WebLN so a 402 page can pay with one click. It uses the official Spark SDK
(no API key). The phrase stays on the device; a site can pay only after
you allow it, and default budgets are 10 sats per page, 100 per site per
day, 500 per day. This is an early prototype: on
mainnet, use only tiny amounts. [Try it](docs/try-the-wallet.md); build
notes are in [extension/README.md](extension/README.md).

## Inspirations and thank you

Spikes402 is dedicated to the books and thinkers that shaped it:

- *Softwar* by Jason P. Lowery
- *The Sovereign Individual* by James Dale Davidson and William Rees-Mogg
- *Democracy: The God That Failed* by Hans-Hermann Hoppe
- *The Virtue of Selfishness* by Ayn Rand

## Donations

If Spikes402 is useful to you, you can support it with bitcoin (on-chain):

<img src="docs/donate-bitcoin.png" alt="QR code for bitcoin:3KvoTk8WfVKE1Dqhh7DHUzTwNyNXeR1GgF" width="180">

`3KvoTk8WfVKE1Dqhh7DHUzTwNyNXeR1GgF`

Lightning (reusable BOLT12 offer; your wallet must support BOLT12/BIP353, e.g. Phoenix):

<img src="docs/donate-lightning.png" alt="QR code for the BOLT12 offer" width="180">

- Address: `₿chromescar71@phoenixwallet.me` (a [BIP353](https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki)
  address; the ₿ only marks it as a bitcoin address, type it as `chromescar71@phoenixwallet.me`)
- Offer: `lno1zrxq8pjw7qjlm68mtp7e3yvxee4y5xrgjhhyf2fxhlphpckrvevh50u0qfq27txqpv7e74w9r9jqytz387aafwc0qq5rduglwu0jgkglqj4kwqszf8me4efvhmywwrekdhtytml88630fxsypfxg52es0l9xrgdz0xlsqv74kqw6557fzsyrdl5wr5vxp9wpu3f4t8shr74qmzjj7ygl8sgpqhvnlkgwzcmgcmyhmctd6vzgmfnfaq07qgqrrv8d4c6ql3dtnnjyka7t47h9lmzgsv2hrnxqdv8u5787m4wfwqqs2n57l9hzh9lyaql207qtwjqe7u`

## Acknowledgements

- Jason Lowery, for [Softwar](https://dspace.mit.edu/handle/1721.1/153030) and the golden retriever with a spiked collar that started it all.
- [PoWned](https://x.com/Printer_Gobrrr/status/2123081612145402056) by @Printer_Gobrrr, the WordPress plugin that makes crawlers mine before they read: the direct inspiration for our proof-of-work path.
- Lightning Labs, for the [L402](https://github.com/lightninglabs/L402) protocol.
- @ang3lsec, for the careful security reports before launch.
- @Malaga82, for the Googlebot reverse-DNS and 1xx ideas.

## License

[GNU AGPLv3](LICENSE).
