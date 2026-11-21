import { describe, expect, it } from "vitest";
import { pickPositions, quizPasses, QUIZ_WORDS } from "./backup";

const phrase = "abandon ability able about above absent absorb abstract absurd abuse access accident";

describe("pickPositions", () => {
  it("returns distinct, sorted positions inside the phrase", () => {
    for (let i = 0; i < 200; i++) {
      const p = pickPositions(12);
      expect(p).toHaveLength(QUIZ_WORDS);
      expect(new Set(p).size).toBe(QUIZ_WORDS);
      expect([...p].sort((a, b) => a - b)).toEqual(p);
      expect(p.every((n) => n >= 0 && n < 12)).toBe(true);
    }
  });

  it("does not always pick the same words", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 50; i++) seen.add(pickPositions(12).join(","));
    expect(seen.size).toBeGreaterThan(5);
  });
});

describe("quizPasses", () => {
  const right = [
    { index: 1, word: "ability" },
    { index: 5, word: "absent" },
    { index: 11, word: "accident" },
  ];

  it("accepts the right words, ignoring case and spaces", () => {
    expect(quizPasses(phrase, right)).toBe(true);
    expect(quizPasses(phrase, [{ index: 1, word: " Ability " }, ...right.slice(1)])).toBe(true);
  });

  it("refuses a wrong word, a wrong position, a repeat, or too few answers", () => {
    expect(quizPasses(phrase, [{ index: 1, word: "abandon" }, ...right.slice(1)])).toBe(false);
    expect(quizPasses(phrase, [{ index: 2, word: "ability" }, ...right.slice(1)])).toBe(false);
    expect(quizPasses(phrase, [right[0], right[0], right[1]])).toBe(false);
    expect(quizPasses(phrase, right.slice(0, 2))).toBe(false);
    expect(quizPasses(phrase, [])).toBe(false);
  });

  it("refuses malformed input", () => {
    expect(quizPasses(phrase, undefined)).toBe(false);
    expect(quizPasses(phrase, "ability")).toBe(false);
    expect(quizPasses(phrase, [null, null, null])).toBe(false);
    expect(quizPasses(phrase, [{ index: 99, word: "x" }, ...right.slice(1)])).toBe(false);
    expect(quizPasses(phrase, [{ index: 1.5, word: "ability" }, ...right.slice(1)])).toBe(false);
  });
});
