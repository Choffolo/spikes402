import type { PaymentRecord, PendingPayment, Settings, WalletInfo } from "../types";
import { invoiceQrSvg } from "./qr";

type Snapshot = { info: WalletInfo; settings: Settings; history: PaymentRecord[]; pending?: PendingPayment };

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

function showMsg(text: string, err = false) {
  const el = $("msg");
  el.hidden = !text;
  el.textContent = text;
  el.classList.toggle("err", err);
}

function call<T>(msg: Record<string, unknown>): Promise<T> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(msg, (res) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (res && typeof res === "object" && "error" in res && res.error) {
        reject(new Error(String(res.error)));
        return;
      }
      resolve(res as T);
    });
  });
}

function show(id: string) {
  for (const sec of Array.from(document.querySelectorAll("section"))) {
    (sec as HTMLElement).hidden = sec.id !== id;
  }
}

async function refresh() {
  showMsg("");
  const snap = await call<Snapshot>({ type: "getState" });
  const { info, settings, history, pending } = snap;
  $("network-flag").textContent = info.network === "mainnet" ? "Network: Bitcoin mainnet" : "Network: Spark regtest (no real money)";

  if (!info.hasVault) {
    show("view-setup");
    return;
  }
  if (!info.unlocked) {
    show("view-unlock");
    return;
  }

  show("view-main");
  $("balance").textContent = info.balanceSats === undefined ? "—" : String(info.balanceSats);
  $("lightning-hint").textContent = info.lightningHint ?? "";
  $("lightning-hint").hidden = !info.lightningHint;

  const pendingBox = $("pending-box");
  if (pending) {
    pendingBox.hidden = false;
    $("pending-text").textContent = `Pay ${pending.amountSats} sats? ${pending.reason}`;
    $("pending-origin").textContent = pending.origin;
    pendingBox.dataset.id = pending.id;
  } else {
    pendingBox.hidden = true;
    delete pendingBox.dataset.id;
  }

  ($("set-per-pay") as HTMLInputElement).value = String(settings.maxSatsPerPayment);
  ($("set-per-site") as HTMLInputElement).value = String(settings.maxSatsPerSitePerDay);
  ($("set-per-day") as HTMLInputElement).value = String(settings.maxSatsPerDay);
  ($("set-autopay") as HTMLInputElement).checked = settings.autoPay;
  ($("set-network") as HTMLSelectElement).value = settings.network;
  ($("set-apikey") as HTMLInputElement).value = settings.breezApiKey;

  const list = $("history");
  list.replaceChildren();
  if (history.length === 0) {
    const li = document.createElement("li");
    li.textContent = "No payments yet.";
    list.append(li);
  } else {
    for (const h of history) {
      const li = document.createElement("li");
      const when = new Date(h.time).toLocaleString();
      li.textContent = `${when} · ${h.result} · ${h.amountSats} sats · ${h.origin} · ${h.detail}`;
      list.append(li);
    }
  }
}

function passwordPair(): string {
  const a = ($("setup-pass") as HTMLInputElement).value;
  const b = ($("setup-pass2") as HTMLInputElement).value;
  if (a.length < 8) throw new Error("Use a password of at least 8 characters.");
  if (a !== b) throw new Error("The two passwords do not match.");
  return a;
}

$("btn-create").addEventListener("click", async () => {
  try {
    showMsg("");
    const password = passwordPair();
    const { mnemonic } = await call<{ mnemonic: string }>({ type: "createWallet", password });
    const list = $("mnemonic-list");
    list.replaceChildren();
    for (const word of mnemonic.split(" ")) {
      const li = document.createElement("li");
      li.textContent = word;
      list.append(li);
    }
    ($("backup-confirm") as HTMLInputElement).checked = false;
    ($("btn-backup-done") as HTMLButtonElement).disabled = true;
    show("view-backup");
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-show-restore").addEventListener("click", () => {
  $("restore-box").hidden = !$("restore-box").hidden;
});

$("btn-restore").addEventListener("click", async () => {
  try {
    showMsg("");
    const password = passwordPair();
    const mnemonic = ($("restore-words") as HTMLTextAreaElement).value;
    await call({ type: "restoreWallet", mnemonic, password });
    await refresh();
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("backup-confirm").addEventListener("change", () => {
  ($("btn-backup-done") as HTMLButtonElement).disabled = !($("backup-confirm") as HTMLInputElement).checked;
});

$("btn-backup-done").addEventListener("click", () => {
  $("mnemonic-list").replaceChildren();
  void refresh();
});

$("btn-unlock").addEventListener("click", async () => {
  try {
    showMsg("");
    const password = ($("unlock-pass") as HTMLInputElement).value;
    await call({ type: "unlock", password });
    ($("unlock-pass") as HTMLInputElement).value = "";
    await refresh();
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-lock").addEventListener("click", async () => {
  await call({ type: "lock" });
  await refresh();
});

$("btn-receive").addEventListener("click", async () => {
  try {
    showMsg("");
    const amountSats = Number(($("recv-amount") as HTMLInputElement).value);
    const res = await call<{ invoice: string }>({ type: "receive", amountSats });
    $("recv-out").hidden = false;
    $("recv-invoice").textContent = res.invoice;
    $("recv-qr").innerHTML = invoiceQrSvg(res.invoice);
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-copy-invoice").addEventListener("click", async () => {
  const text = $("recv-invoice").textContent ?? "";
  await navigator.clipboard.writeText(text);
  showMsg("Invoice copied.");
});

$("btn-send").addEventListener("click", async () => {
  try {
    showMsg("");
    const destination = ($("send-dest") as HTMLTextAreaElement).value;
    const raw = ($("send-amount") as HTMLInputElement).value;
    const amountSats = raw ? Number(raw) : undefined;
    const res = await call<{ detail: string }>({ type: "send", destination, amountSats });
    showMsg(res.detail || "Sent.");
    await refresh();
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-save-budget").addEventListener("click", async () => {
  try {
    await call({
      type: "saveSettings",
      settings: {
        maxSatsPerPayment: Number(($("set-per-pay") as HTMLInputElement).value),
        maxSatsPerSitePerDay: Number(($("set-per-site") as HTMLInputElement).value),
        maxSatsPerDay: Number(($("set-per-day") as HTMLInputElement).value),
        autoPay: ($("set-autopay") as HTMLInputElement).checked,
      },
    });
    showMsg("Budgets saved.");
    await refresh();
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-save-network").addEventListener("click", async () => {
  try {
    await call({
      type: "saveSettings",
      settings: {
        network: ($("set-network") as HTMLSelectElement).value,
        breezApiKey: ($("set-apikey") as HTMLInputElement).value.trim(),
      },
    });
    showMsg("Network settings saved. Lock and unlock the wallet to apply them.");
    await refresh();
  } catch (e) {
    showMsg(e instanceof Error ? e.message : String(e), true);
  }
});

$("btn-accept").addEventListener("click", async () => {
  const id = $("pending-box").dataset.id;
  if (!id) return;
  await call({ type: "confirmPayment", id, accept: true });
  await refresh();
});

$("btn-decline").addEventListener("click", async () => {
  const id = $("pending-box").dataset.id;
  if (!id) return;
  await call({ type: "confirmPayment", id, accept: false });
  await refresh();
});

document.addEventListener("DOMContentLoaded", () => {
  void refresh().catch((e) => showMsg(e instanceof Error ? e.message : String(e), true));
});
void refresh().catch((e) => showMsg(e instanceof Error ? e.message : String(e), true));
