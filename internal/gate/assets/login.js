// Spikes402 login proof-of-work, served by the proxy at /.spikes402/login.js.
// Add one tag to the login page (the field is the username input's name):
//
//   <script src="/.spikes402/login.js" data-field="username" defer></script>
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
  var WORKER = "__WORKER__";

  function solve(ch) {
    return new Promise(function (resolve, reject) {
      var mem = navigator.deviceMemory || 4;
      var n = Math.max(1, Math.min(navigator.hardwareConcurrency || 2, mem < 4 ? 2 : 8));
      var workers = [];
      function stop() { workers.forEach(function (w) { w.terminate(); }); }
      try {
        for (var i = 0; i < n; i++) {
          var w = new Worker(WORKER);
          w.onmessage = function (e) {
            if (e.data.nonce !== undefined) { stop(); resolve(ch.challenge + ":" + e.data.nonce); }
          };
          w.onerror = function () { stop(); reject(new Error("worker failed")); };
          w.postMessage({ challenge: ch.challenge, difficulty: ch.difficulty, memoryMiB: ch.memory_mib, start: i, step: n });
          workers.push(w);
        }
      } catch (err) { stop(); reject(err); }
    });
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

    function start() {
      var user = input.value;
      if (!user) return null;
      if (job && job.user === user && Date.now() < job.until) return job.proof;
      var u = new URL(form.getAttribute("action") || location.href, location.href);
      var body = JSON.stringify({ method: (form.getAttribute("method") || "POST").toUpperCase(), path: u.pathname, username: user });
      var proof = fetch("/.spikes402/login-challenge", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: body, credentials: "same-origin", cache: "no-store"
      }).then(function (r) {
        if (!r.ok) throw new Error("challenge refused (" + r.status + ")");
        return r.json();
      }).then(function (ch) {
        j.until = Date.parse(ch.expires_at) - 10000;
        return solve(ch);
      });
      var j = job = { user: user, until: Date.now() + 110000, proof: proof };
      proof.catch(function () { if (job === j) job = null; });
      return proof;
    }

    input.addEventListener("change", start);
    input.addEventListener("blur", start);
    form.addEventListener("submit", function (e) {
      if (ready) { ready = false; return; } // our own resubmission: let it go
      e.preventDefault();
      var submitter = e.submitter;
      if (busy) return;
      var proof = start();
      if (!proof) return;
      busy = true;
      status.textContent = "Checking this login with a short proof-of-work…";
      proof.then(function (p) {
        return fetch("/.spikes402/login-redeem", {
          method: "POST", headers: { Authorization: "PoW " + p }, credentials: "same-origin", cache: "no-store"
        });
      }).then(function (r) {
        job = null; // one proof per attempt
        if (r.status !== 204) throw new Error("proof refused (" + r.status + ")");
        status.textContent = "";
        ready = true;
        if (form.requestSubmit) form.requestSubmit(submitter && submitter.form === form ? submitter : undefined);
        else HTMLFormElement.prototype.submit.call(form);
      }).catch(function (err) {
        job = null;
        status.textContent = "The login check did not work (" + err.message + "). Please try again.";
      }).then(function () { busy = false; });
    });
  }

  Array.prototype.forEach.call(document.querySelectorAll("form"), setup);
})();
