import { describe, expect, it } from "vitest";
import { decryptMnemonic, encryptMnemonic, generateMnemonic, isValidMnemonic, normalizeMnemonic } from "./crypto";

describe("mnemonic and vault", () => {
  it("generates a valid 12-word mnemonic and rejects junk", () => {
    const m = generateMnemonic();
    expect(m.split(" ")).toHaveLength(12);
    expect(isValidMnemonic(m)).toBe(true);
    expect(isValidMnemonic("not a real mnemonic phrase at all here now")).toBe(false);
  });

  it("normalises spacing and case", () => {
    const m = generateMnemonic();
    expect(normalizeMnemonic(`  ${m.toUpperCase().replace(/ /g, "  ")} \n`)).toBe(m);
  });

  it("round-trips through the password and fails on a wrong one", async () => {
    const m = generateMnemonic();
    const vault = await encryptMnemonic(m, "correct horse");
    expect(vault.kdf).toBe("pbkdf2-sha256");
    expect(vault.iter).toBe(600_000);
    expect(await decryptMnemonic(vault, "correct horse")).toBe(m);
    await expect(decryptMnemonic(vault, "wrong password")).rejects.toThrow(/Wrong password/);
  });

  it("refuses a short password", async () => {
    await expect(encryptMnemonic(generateMnemonic(), "short")).rejects.toThrow(/8 characters/);
  });
});
