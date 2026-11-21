/**
 * Proof that the owner saved the recovery words: a few random positions
 * of the phrase must be typed back. The positions are chosen by the popup,
 * but the check runs where the phrase is decrypted (the wallet host), and
 * it refuses a quiz that is too short to mean anything.
 */
export const QUIZ_WORDS = 3;

export type QuizAnswer = { index: number; word: string };

/** `count` different positions out of `total`, in ascending order. `rand` returns a number in [0, 1). */
export function pickPositions(total: number, count = QUIZ_WORDS, rand: () => number = secureRandom): number[] {
  if (!Number.isInteger(total) || total < count) throw new Error("Not enough words to check.");
  const all = Array.from({ length: total }, (_, i) => i);
  for (let i = all.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [all[i], all[j]] = [all[j], all[i]];
  }
  return all.slice(0, count).sort((a, b) => a - b);
}

/** True only if at least QUIZ_WORDS different positions are given and every word matches. */
export function quizPasses(mnemonic: string, answers: unknown): boolean {
  if (!Array.isArray(answers)) return false;
  const words = mnemonic.trim().split(/\s+/);
  const seen = new Set<number>();
  for (const a of answers) {
    if (!a || typeof a !== "object") return false;
    const { index, word } = a as Partial<QuizAnswer>;
    if (typeof index !== "number" || !Number.isInteger(index) || index < 0 || index >= words.length) return false;
    if (typeof word !== "string" || seen.has(index)) return false;
    if (word.trim().toLowerCase() !== words[index]) return false;
    seen.add(index);
  }
  return seen.size >= QUIZ_WORDS;
}

function secureRandom(): number {
  const b = new Uint32Array(1);
  crypto.getRandomValues(b);
  return b[0] / 2 ** 32;
}
