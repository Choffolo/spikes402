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
(function () {
  "use strict";
  var me = document.currentScript;
  var field = (me && me.getAttribute("data-field")) || "username";
  var textWorking = (me && me.getAttribute("data-text-working")) || "Checking this login with a short proof-of-work…";
  var textFailed = (me && me.getAttribute("data-text-failed")) || "The login check did not work ({error}). Please try again.";

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

  // run starts one worker and resolves with the first nonce it finds.
  function run(url, msg, workers) {
    return new Promise(function (resolve, reject) {
      var w = new Worker(url);
      workers.push(w);
      w.onmessage = function (e) { if (e.data.nonce !== undefined) { w.terminate(); resolve(e.data.nonce); } };
      w.onerror = function () { reject(new Error("worker failed")); };
      w.postMessage(msg);
    });
  }

  // solve finds the Argon2id nonce (several workers) and the sha256d
  // pre-check (one worker) at the same time: "challenge:nonce:pre".
  function solve(ch) {
    var argonW = [], preW = [];
    function stop(list) { list.forEach(function (w) { w.terminate(); }); }
    var mem = navigator.deviceMemory || 4;
    var n = Math.max(1, Math.min(navigator.hardwareConcurrency || 2, mem < 4 ? 2 : 8));
    var argon = [], pre;
    try {
      for (var i = 0; i < n; i++) {
        argon.push(run(WORKER, { challenge: ch.challenge, difficulty: ch.difficulty, memoryMiB: ch.memory_mib, start: i, step: n }, argonW));
      }
      pre = run(POW_WORKER, { challenge: ch.challenge, difficulty: ch.precheck_difficulty, start: 0 }, preW);
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
    var job = null, busy = false, ready = false;
    var status = document.createElement("p");
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    form.appendChild(status);

    function start(submitter) {
      var user = input.value;
      if (!user) return null;
      var route = routeOf(form, submitter);
      var key = route.method + " " + route.path + " " + user;
      if (job && job.key === key && Date.now() < job.until) return job.proof;
      var body = JSON.stringify({ method: route.method, path: route.path, username: user });
      var proof = fetch("/.spikes402/login-challenge", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: body, credentials: "same-origin", cache: "no-store"
      }).then(function (r) {
        if (!r.ok) throw new Error("challenge refused (" + r.status + ")");
        return r.json();
      }).then(function (ch) {
        j.until = Date.parse(ch.expires_at) - 10000;
        return solve(ch);
      });
      var j = job = { key: key, until: Date.now() + 110000, proof: proof };
      proof.catch(function () { if (job === j) job = null; });
      return proof;
    }

    input.addEventListener("change", function () { start(); });
    input.addEventListener("blur", function () { start(); });
    form.addEventListener("submit", function (e) {
      if (ready) { ready = false; return; } // our own resubmission: let it go
      e.preventDefault();
      var submitter = e.submitter;
      if (busy) return;
      var proof = start(submitter);
      if (!proof) return;
      busy = true;
      status.textContent = textWorking;
      proof.then(function (p) {
        return fetch("/.spikes402/login-redeem", {
          method: "POST", headers: { Authorization: "PoW " + p }, credentials: "same-origin", cache: "no-store"
        });
      }).then(function (r) {
        job = null; // one proof per attempt
        if (r.status !== 204) throw new Error("proof refused (" + r.status + ")");
        status.textContent = "";
        if (form.requestSubmit) {
          ready = true; // requestSubmit fires "submit" synchronously, unless validation fails
          try { form.requestSubmit(submitter && submitter.form === form ? submitter : undefined); } finally { ready = false; }
        } else HTMLFormElement.prototype.submit.call(form);
      }).catch(function (err) {
        job = null;
        status.textContent = textFailed.replace("{error}", err.message);
      }).then(function () { busy = false; });
    });
  }

  Array.prototype.forEach.call(document.querySelectorAll("form"), setup);
})();
