# Security

## Reporting a vulnerability

Please do not report security problems in public issues.

- Preferred: GitHub private vulnerability reporting, under the repository's
  **Security** tab → **Report a vulnerability**.
- If that button is not available, open an issue titled "Security contact"
  **without any details**, and we will reply with a private channel.

Include the version or commit, your configuration (without secrets) and the
steps to reproduce. Please give us a
reasonable time to release a fix before publishing.

Only the latest commit on `main` gets security fixes.

## Running it safely

- Put Spikes402 behind your own TLS reverse proxy, list only that proxy in
  `trusted_proxies`, and keep the admin dashboard on `127.0.0.1`.
- Keep withdrawals off. While they are on, the internet-facing process holds
  phoenixd's full-access password, which can spend all funds; a bug there
  could empty the node. Withdraw with phoenixd's own tools instead, or turn
  withdrawals on only for the time you need them.
- Keep the node's balance low; move earnings out regularly.
- Keep `ai_can_apply` off (see below).

## Known limitations

These are known and not fixed yet. Contributions are welcome.

- **Rate limiter overflow.** Clients are tracked individually up to 100,000
  addresses a minute; beyond that, new clients share one bucket and get
  `429`. An attacker with that many addresses (easy with IPv6) can lock out
  new visitors for a while. The Lightning node stays protected.
- **AI admin with `ai_can_apply`.** When enabled, the AI admin can change
  policies and prices, including making pages free, and it reads text chosen
  by visitors (paths, bot host names), which could carry prompt injection.
  It cannot withdraw or approve proposals. Leave `ai_can_apply` off, or
  review its changes.
- **Key-directory fetches (Web Bot Auth).** The fetcher only connects over
  https, on port 443, to public addresses, but a few rarely used special
  ranges (for example NAT64 `64:ff9b::/96`, 6to4, Teredo, `198.18.0.0/15`,
  `240.0.0.0/4`) are not blocked yet. The fetch budget is global, so anyone
  can use it up and delay the discovery of a new, legitimate bot directory
  (that bot then just pays like everyone else).
- **Spent-token store full.** Up to 1,000,000 unexpired tokens are
  remembered to stop replays. If an attacker fills it (it takes a large,
  sustained number of valid redemptions from many addresses), new
  redemptions, including paid ones, get "server busy" until entries expire.
- **Argon2id memory.** Up to 4 answers are checked at once, each using
  `argon2_memory_mib`. With the maximum (256 MiB) a burst needs about 1 GiB
  of RAM. The default (16 MiB) needs about 64 MiB.
