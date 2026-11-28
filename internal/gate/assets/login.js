// Spikes402 login proof-of-work, served by the proxy at /.spikes402/login.js.
// Add one tag to the login page (the field is the username input's name):
//
//   <script src="/.spikes402/login.js" data-field="username" defer></script>
//
// Optional: data-text-working and data-text-failed replace the status
// messages (data-text-failed may contain {error}).
//
// When the username field loses focus, the script asks the proxy for a
// challenge bound to that account and solves it (Argon2id, in Web Workers)
// while the password is typed. On submit it trades the solution for a
// one-use login cookie and submits the form as usual. The proxy enforces
// everything: without this script, a login simply does not get through.
//
// If the site lets logins be paid for instead, a "Pay instead" button
// appears: it shows a Lightning invoice (QR code, text, and a one-click
// button when a WebLN wallet such as the Spikes402 extension is present).
// Paying replaces only the computation of this attempt; the username and
// password still have to be right.
(function () {
  "use strict";
  var me = document.currentScript;
  var field = (me && me.getAttribute("data-field")) || "username";
  var textWorking = (me && me.getAttribute("data-text-working")) || "Checking this login with a short proof-of-work…";
  var textFailed = (me && me.getAttribute("data-text-failed")) || "The login check did not work ({error}). Please try again.";
  var textPay = (me && me.getAttribute("data-text-pay")) || "Pay {sats} sats instead";

  // routeOf is where a submission goes, as HTML decides it: the
  // submitter's formaction / formmethod first, then the form's action and
  // method; no method means GET.
  function routeOf(form, submitter) {
    var s = submitter && submitter.form === form ? submitter : null;
    var action = (s && s.getAttribute("formaction")) || form.getAttribute("action") || location.href;
    var method = (s && s.getAttribute("formmethod")) || form.getAttribute("method") || "GET";
    return { method: method.toUpperCase(), path: new URL(action, location.href).pathname };
  }
  var WORKER = "__WORKER__", POW_WORKER = "__POW_WORKER__";

  // run starts one worker and resolves with the first nonce it finds. It
  // adds to each list a function that stops the worker (and rejects).
  function run(url, msg, lists) {
    return new Promise(function (resolve, reject) {
      var w = new Worker(url);
      var kill = function () { w.terminate(); reject(new Error("stopped")); };
      lists.forEach(function (l) { l.push(kill); });
      w.onmessage = function (e) { if (e.data.nonce !== undefined) { w.terminate(); resolve(e.data.nonce); } };
      w.onerror = function () { reject(new Error("worker failed")); };
      w.postMessage(msg);
    });
  }

  function stop(list) { list.forEach(function (kill) { kill(); }); }

  // solve finds the Argon2id nonce (several workers) and the sha256d
  // pre-check (one worker) at the same time: "challenge:nonce:pre".
  // job.kills gets every worker, so a replaced job stops its work.
  function solve(ch, job) {
    var argonW = [], preW = [];
    var mem = navigator.deviceMemory || 4;
    var n = Math.max(1, Math.min(navigator.hardwareConcurrency || 2, mem < 4 ? 2 : 8));
    var argon = [], pre;
    try {
      for (var i = 0; i < n; i++) {
        argon.push(run(WORKER, { challenge: ch.challenge, difficulty: ch.difficulty, memoryMiB: ch.memory_mib, start: i, step: n }, [argonW, job.kills]));
      }
      pre = run(POW_WORKER, { challenge: ch.challenge, difficulty: ch.precheck_difficulty, start: 0 }, [preW, job.kills]);
    } catch (err) { stop(argonW); stop(preW); return Promise.reject(err); }
    var nonce = Promise.race(argon).then(function (v) { stop(argonW); return v; });
    return Promise.all([nonce, pre]).then(function (r) {
      return ch.challenge + ":" + r[0] + ":" + r[1];
    }, function (err) { stop(argonW); stop(preW); throw err; });
  }

  function setup(form) {
    var input = form.elements[field];
    if (!input || form.getAttribute("data-spikes402")) return;
    form.setAttribute("data-spikes402", "1");
    var job = null, busy = false, ready = false, paid = null;
    var status = document.createElement("p");
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    form.appendChild(status);
    var payBtn = document.createElement("button");
    payBtn.type = "button";
    payBtn.hidden = true;
    form.appendChild(payBtn);
    var payBox = document.createElement("div");
    payBox.hidden = true;
    form.appendChild(payBox);
    var payJob = null;

    function offerPay(ch) {
      payBtn.hidden = !ch.pay_instead;
      if (ch.pay_instead) payBtn.textContent = textPay.replace("{sats}", ch.price_sats);
    }

    function submitNow(submitter) {
      if (form.requestSubmit) {
        ready = true; // requestSubmit fires "submit" synchronously, unless validation fails
        try { form.requestSubmit(submitter && submitter.form === form ? submitter : undefined); } finally { ready = false; }
      } else HTMLFormElement.prototype.submit.call(form);
    }

    // pay asks for an invoice for this account and waits until it is paid
    // (polling, or the preimage from a WebLN wallet), then logs in with the
    // one-use pass it buys.
    function pay() {
      var user = input.value;
      if (!user || busy) return;
      var route = routeOf(form, null), key = route.method + " " + route.path + " " + user;
      var mine = payJob = { stopped: false };
      payBox.textContent = "";
      payBox.hidden = false;
      status.textContent = "";
      fetch("/.spikes402/login-invoice", {
        method: "POST", headers: { "Content-Type": "application/json" }, credentials: "same-origin", cache: "no-store",
        body: JSON.stringify({ method: route.method, path: route.path, username: user })
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (inv) {
          if (r.status !== 200 || !inv.invoice) throw new Error(inv.error || "no invoice (" + r.status + ")");
          return inv;
        });
      }).then(function (inv) {
        if (payJob !== mine) return;
        var link = document.createElement("a");
        link.href = "lightning:" + inv.invoice;
        var img = document.createElement("img");
        img.src = inv.qr; img.width = 160; img.height = 160; img.alt = "QR code of the Lightning invoice";
        link.appendChild(img);
        var text = document.createElement("code");
        text.textContent = inv.invoice;
        text.style.wordBreak = "break-all";
        var note = document.createElement("p");
        note.textContent = "Pay " + inv.price_sats + " sats to try this login once. The password must still be right. " +
          "Warning: this price holds only if no other login attempt on this account, or from your network, fails before you pay. " +
          "If one does, this payment buys nothing and is not refunded.";
        payBox.append(note, link, text);
        var redeem = function (auth) {
          return fetch("/.spikes402/login-redeem", { method: "POST", headers: { Authorization: auth }, credentials: "same-origin", cache: "no-store" });
        };
        var done = function (r) {
          if (payJob !== mine || mine.stopped) return true;
          if (r.status === 202 || r.status === 429 || r.status === 503) return false; // not paid yet, or busy: keep polling
          mine.stopped = true;
          if (r.status !== 204) {
            r.json().catch(function () { return {}; }).then(function (b) {
              if (payJob !== mine) return;
              if (r.status === 409 && b.error) { // the level rose: this payment buys nothing
                payBox.hidden = true;
                offerPay({ pay_instead: b.price_sats > 0, price_sats: b.price_sats });
                status.textContent = b.error.charAt(0).toUpperCase() + b.error.slice(1) + ".";
              } else fail(new Error(b.error || "payment not accepted (" + r.status + ")"));
            });
            return true;
          }
          if (job) { stop(job.kills); job = null; }
          payBox.hidden = true;
          payBox.textContent = "";
          paid = { key: key, until: Date.now() + 55000 };
          status.textContent = "Paid. Logging in…";
          submitNow(null);
          return true;
        };
        var fail = function (err) {
          if (payJob !== mine) return;
          mine.stopped = true;
          status.textContent = textFailed.replace("{error}", err.message);
        };
        var w = window.webln;
        if (w && typeof w.enable === "function" && typeof w.sendPayment === "function") {
          var one = document.createElement("button");
          one.type = "button";
          one.textContent = "Pay with one click";
          one.addEventListener("click", function () {
            one.disabled = true;
            Promise.resolve().then(function () { return w.enable(); }).then(function () { return w.sendPayment(inv.invoice); }).then(function (res) {
              var pre = ((res && (res.preimage || res.paymentPreimage)) || "").toString().replace(/^0x/i, "").replace(/\s/g, "");
              return redeem("L402 " + inv.challenge + (pre ? ":" + pre : ""));
            }).then(done).catch(function (err) { one.disabled = false; fail(err); });
          });
          payBox.append(one);
        }
        var until = Date.parse(inv.expires_at);
        (function poll() {
          if (payJob !== mine || mine.stopped) return;
          if (Date.now() > until) { fail(new Error("invoice expired")); return; }
          redeem("L402 " + inv.challenge).then(function (r) {
            if (!done(r)) setTimeout(poll, 2500);
          }).catch(fail);
        })();
      }).catch(function (err) {
        if (payJob !== mine) return;
        payBox.hidden = true;
        status.textContent = textFailed.replace("{error}", err.message);
      });
    }
    payBtn.addEventListener("click", pay);

    function start(submitter) {
      var user = input.value;
      if (!user) return null;
      var route = routeOf(form, submitter);
      var key = route.method + " " + route.path + " " + user;
      if (job && job.key === key && Date.now() < job.until) return job.proof;
      if (job) stop(job.kills); // another user or route, or expired: stop its work
      var body = JSON.stringify({ method: route.method, path: route.path, username: user });
      var proof = fetch("/.spikes402/login-challenge", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: body, credentials: "same-origin", cache: "no-store"
      }).then(function (r) {
        if (!r.ok) throw new Error("challenge refused (" + r.status + ")");
        return r.json();
      }).then(function (ch) {
        j.until = Date.parse(ch.expires_at) - 10000;
        offerPay(ch);
        if (job !== j) throw new Error("replaced");
        return solve(ch, j);
      });
      var j = job = { key: key, until: Date.now() + 110000, proof: proof, kills: [] };
      proof.catch(function () { if (job === j) job = null; });
      return proof;
    }

    input.addEventListener("change", function () { start(); });
    input.addEventListener("blur", function () { start(); });
    form.addEventListener("submit", function (e) {
      if (ready) { ready = false; return; } // our own resubmission: let it go
      var submitter = e.submitter;
      if (paid) {
        var r0 = routeOf(form, submitter), p0 = paid;
        paid = null;
        if (p0.key === r0.method + " " + r0.path + " " + input.value && Date.now() < p0.until) return; // a paid pass is waiting
      }
      e.preventDefault();
      if (busy) return;
      var proof = start(submitter), mine = job;
      if (!proof) return;
      busy = true;
      status.textContent = textWorking;
      proof.then(function (p) {
        return fetch("/.spikes402/login-redeem", {
          method: "POST", headers: { Authorization: "PoW " + p }, credentials: "same-origin", cache: "no-store"
        });
      }).then(function (r) {
        if (job === mine) job = null; // one proof per attempt
        if (r.status !== 204) throw new Error("proof refused (" + r.status + ")");
        status.textContent = "";
        submitNow(submitter);
      }).catch(function (err) {
        if (job === mine) { stop(mine.kills); job = null; }
        status.textContent = textFailed.replace("{error}", err.message);
      }).then(function () { busy = false; });
    });
  }

  Array.prototype.forEach.call(document.querySelectorAll("form"), setup);
})();
