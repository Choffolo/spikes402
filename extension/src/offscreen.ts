import { sparkBackend } from "./spark";
import { WalletError } from "./backend";

type WalletMsg =
  | { target: "wallet"; type: "connect"; mnemonic: string; network: "regtest" | "mainnet" }
  | { target: "wallet"; type: "disconnect" }
  | { target: "wallet"; type: "getInfo" }
  | { target: "wallet"; type: "receive"; amountSats: number }
  | { target: "wallet"; type: "send"; destination: string; amountSats?: number }
  | { target: "wallet"; type: "status" };

let unlocked = false;

chrome.runtime.onMessage.addListener((msg: WalletMsg, _sender, sendResponse) => {
  if (!msg || msg.target !== "wallet") return;
  handle(msg)
    .then(sendResponse)
    .catch((e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
  return true;
});

async function handle(msg: WalletMsg): Promise<unknown> {
  switch (msg.type) {
    case "status":
      return { unlocked };
    case "connect":
      await sparkBackend.connect({ mnemonic: msg.mnemonic, network: msg.network });
      unlocked = true;
      return { ok: true };
    case "disconnect":
      await sparkBackend.disconnect();
      unlocked = false;
      return { ok: true };
    case "getInfo":
      if (!unlocked) throw new WalletError("The wallet is locked.");
      return sparkBackend.getInfo();
    case "receive":
      if (!unlocked) throw new WalletError("The wallet is locked.");
      return sparkBackend.receiveLightning(msg.amountSats);
    case "send":
      if (!unlocked) throw new WalletError("The wallet is locked.");
      return sparkBackend.send(msg.destination, msg.amountSats);
    default:
      throw new WalletError("Unknown wallet message");
  }
}
