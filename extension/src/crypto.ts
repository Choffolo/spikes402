import { generateMnemonic as bip39generate, validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english.js";

const PBKDF2_ITERATIONS = 600_000;
const SALT_BYTES = 16;
const IV_BYTES = 12;
const KEY_BITS = 256;

export type EncryptedVault = {
  v: 1;
  kdf: "pbkdf2-sha256";
  iter: number;
  salt: string; // base64
  iv: string;
  ciphertext: string;
};

export function generateMnemonic(): string {
  return bip39generate(wordlist, 128); // 12 words
}

export function normalizeMnemonic(s: string): string {
  return s.trim().toLowerCase().split(/\s+/).filter(Boolean).join(" ");
}

export function isValidMnemonic(s: string): boolean {
  return validateMnemonic(normalizeMnemonic(s), wordlist);
}

export async function encryptMnemonic(mnemonic: string, password: string): Promise<EncryptedVault> {
  if (password.length < 8) throw new Error("Password must be at least 8 characters.");
  const salt = crypto.getRandomValues(new Uint8Array(SALT_BYTES));
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const key = await deriveKey(password, salt, PBKDF2_ITERATIONS);
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(mnemonic));
  return {
    v: 1,
    kdf: "pbkdf2-sha256",
    iter: PBKDF2_ITERATIONS,
    salt: b64(salt),
    iv: b64(iv),
    ciphertext: b64(new Uint8Array(ciphertext)),
  };
}

export async function decryptMnemonic(vault: EncryptedVault, password: string): Promise<string> {
  if (vault.v !== 1 || vault.kdf !== "pbkdf2-sha256") {
    throw new Error("This vault was written by a newer version of the extension.");
  }
  const salt = fromb64(vault.salt);
  const iv = fromb64(vault.iv);
  const key = await deriveKey(password, salt, vault.iter);
  try {
    const pt = await crypto.subtle.decrypt({ name: "AES-GCM", iv }, key, fromb64(vault.ciphertext));
    return new TextDecoder().decode(pt);
  } catch {
    throw new Error("Wrong password.");
  }
}

async function deriveKey(password: string, salt: Uint8Array<ArrayBuffer>, iter: number): Promise<CryptoKey> {
  const material = await crypto.subtle.importKey("raw", new TextEncoder().encode(password), "PBKDF2", false, [
    "deriveKey",
  ]);
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", salt, iterations: iter, hash: "SHA-256" },
    material,
    { name: "AES-GCM", length: KEY_BITS },
    false,
    ["encrypt", "decrypt"],
  );
}

function b64(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

function fromb64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
