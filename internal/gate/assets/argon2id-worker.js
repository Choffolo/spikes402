// Spikes402 Argon2id proof-of-work worker, served by the proxy at
// /.spikes402/argon2id-worker.js. No dependencies, no network.
//
// Finds a nonce so that Argon2id(password = challenge + ":" + nonce,
// salt = "spikes402-pow-v1", time = 1, memory = memoryMiB MiB, threads = 1,
// 32 bytes) has at least `difficulty` leading zero bits: the same puzzle as
// pow.Params.Check. Argon2 v1.3 (RFC 9106) and BLAKE2b are written with
// 32-bit halves, because JavaScript has no fast 64-bit integers.
//
// In:  {challenge, difficulty, memoryMiB, start, step}
// Out: {attempts}          progress, after every attempt
//      {attempts, nonce}   solved
(function () {
"use strict";

// ---- BLAKE2b ----
var IV = new Uint32Array([
  0xf3bcc908, 0x6a09e667, 0x84caa73b, 0xbb67ae85, 0xfe94f82b, 0x3c6ef372, 0x5f1d36f1, 0xa54ff53a,
  0xade682d1, 0x510e527f, 0x2b3e6c1f, 0x9b05688c, 0xfb41bd6b, 0x1f83d9ab, 0x137e2179, 0x5be0cd19
]);
var SIGMA = [
  0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
  14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3,
  11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4,
  7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8,
  9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13,
  2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9,
  12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11,
  13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10,
  6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5,
  10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0
];
var bv = new Uint32Array(32), bm = new Uint32Array(32);

// v[a] += v[b] + m[x], on 64-bit words stored as (lo, hi) pairs.
function add3(a, b, x) {
  var lo = bv[a] + bv[b] + bm[x];
  bv[a + 1] = (bv[a + 1] + bv[b + 1] + bm[x + 1] + (lo / 0x100000000 | 0)) >>> 0;
  bv[a] = lo >>> 0;
}
function b2g(a, b, c, d, x, y) {
  var lo, hi;
  add3(a, b, x);
  lo = bv[d] ^ bv[a]; hi = bv[d + 1] ^ bv[a + 1]; bv[d] = hi; bv[d + 1] = lo; // >>> 32
  lo = bv[c] + bv[d]; bv[c + 1] = (bv[c + 1] + bv[d + 1] + (lo / 0x100000000 | 0)) >>> 0; bv[c] = lo >>> 0;
  lo = bv[b] ^ bv[c]; hi = bv[b + 1] ^ bv[c + 1];
  bv[b] = (lo >>> 24 | hi << 8) >>> 0; bv[b + 1] = (hi >>> 24 | lo << 8) >>> 0;
  add3(a, b, y);
  lo = bv[d] ^ bv[a]; hi = bv[d + 1] ^ bv[a + 1];
  bv[d] = (lo >>> 16 | hi << 16) >>> 0; bv[d + 1] = (hi >>> 16 | lo << 16) >>> 0;
  lo = bv[c] + bv[d]; bv[c + 1] = (bv[c + 1] + bv[d + 1] + (lo / 0x100000000 | 0)) >>> 0; bv[c] = lo >>> 0;
  lo = bv[b] ^ bv[c]; hi = bv[b + 1] ^ bv[c + 1];
  bv[b] = (lo << 1 | hi >>> 31) >>> 0; bv[b + 1] = (hi << 1 | lo >>> 31) >>> 0; // >>> 63
}
function b2compress(h, block, off, t, last) {
  var i;
  for (i = 0; i < 16; i++) { bv[i] = h[i]; bv[i + 16] = IV[i]; }
  bv[24] ^= t >>> 0; bv[25] ^= t / 0x100000000 >>> 0;
  if (last) { bv[28] = ~bv[28] >>> 0; bv[29] = ~bv[29] >>> 0; }
  for (i = 0; i < 32; i++) {
    var o = off + i * 4;
    bm[i] = (block[o] | block[o + 1] << 8 | block[o + 2] << 16 | block[o + 3] << 24) >>> 0;
  }
  for (i = 0; i < 12; i++) {
    var s = (i % 10) * 16;
    b2g(0, 8, 16, 24, SIGMA[s] * 2, SIGMA[s + 1] * 2);
    b2g(2, 10, 18, 26, SIGMA[s + 2] * 2, SIGMA[s + 3] * 2);
    b2g(4, 12, 20, 28, SIGMA[s + 4] * 2, SIGMA[s + 5] * 2);
    b2g(6, 14, 22, 30, SIGMA[s + 6] * 2, SIGMA[s + 7] * 2);
    b2g(0, 10, 20, 30, SIGMA[s + 8] * 2, SIGMA[s + 9] * 2);
    b2g(2, 12, 22, 24, SIGMA[s + 10] * 2, SIGMA[s + 11] * 2);
    b2g(4, 14, 16, 26, SIGMA[s + 12] * 2, SIGMA[s + 13] * 2);
    b2g(6, 8, 18, 28, SIGMA[s + 14] * 2, SIGMA[s + 15] * 2);
  }
  for (i = 0; i < 16; i++) h[i] = (h[i] ^ bv[i] ^ bv[i + 16]) >>> 0;
}
// blake2b returns the BLAKE2b hash (no key) of input with outlen bytes.
function blake2b(input, outlen) {
  var h = new Uint32Array(IV.subarray(0, 16)), n = input.length, off = 0, i;
  h[0] ^= 0x01010000 ^ outlen;
  var block = new Uint8Array(128);
  while (n - off > 128) { b2compress(h, input, off, off + 128, false); off += 128; }
  block.set(input.subarray(off));
  b2compress(h, block, 0, n, true);
  var out = new Uint8Array(outlen);
  for (i = 0; i < outlen; i++) out[i] = h[i >> 2] >>> (8 * (i & 3)) & 0xff;
  return out;
}
function le32(x) { return new Uint8Array([x & 0xff, x >>> 8 & 0xff, x >>> 16 & 0xff, x >>> 24 & 0xff]); }
function concat(parts) {
  var n = 0, i, o = 0;
  for (i = 0; i < parts.length; i++) n += parts[i].length;
  var out = new Uint8Array(n);
  for (i = 0; i < parts.length; i++) { out.set(parts[i], o); o += parts[i].length; }
  return out;
}
// hprime is Argon2's variable-length hash H'.
function hprime(input, outlen) {
  var x = concat([le32(outlen), input]);
  if (outlen <= 64) return blake2b(x, outlen);
  var out = new Uint8Array(outlen), v = blake2b(x, 64), o = 0;
  while (outlen - o > 64) { out.set(v.subarray(0, 32), o); o += 32; v = blake2b(v, outlen - o > 64 ? 64 : outlen - o); }
  out.set(v, o);
  return out;
}

// ---- Argon2 block compression (BlaMka) ----
var R = new Uint32Array(256), Z = new Uint32Array(256);
// t[a] += t[b] + 2 * lo32(t[a]) * lo32(t[b])
function fma(t, a, b) {
  var x = t[a], y = t[b];
  var x0 = x & 0xffff, x1 = x >>> 16, y0 = y & 0xffff, y1 = y >>> 16;
  var p00 = x0 * y0, p01 = x0 * y1, p10 = x1 * y0;
  var mid = (p00 >>> 16) + (p01 & 0xffff) + (p10 & 0xffff);
  var plo = ((mid << 16) | (p00 & 0xffff)) >>> 0;
  var phi = (x1 * y1 + (p01 >>> 16) + (p10 >>> 16) + (mid >>> 16)) >>> 0;
  phi = (phi << 1 | plo >>> 31) >>> 0; plo = plo << 1 >>> 0;
  var lo = x + y + plo;
  t[a + 1] = (t[a + 1] + t[b + 1] + phi + (lo / 0x100000000 | 0)) >>> 0;
  t[a] = lo >>> 0;
}
function gb(t, a, b, c, d) {
  var lo, hi;
  fma(t, a, b);
  lo = t[d] ^ t[a]; hi = t[d + 1] ^ t[a + 1]; t[d] = hi; t[d + 1] = lo;
  fma(t, c, d);
  lo = t[b] ^ t[c]; hi = t[b + 1] ^ t[c + 1];
  t[b] = (lo >>> 24 | hi << 8) >>> 0; t[b + 1] = (hi >>> 24 | lo << 8) >>> 0;
  fma(t, a, b);
  lo = t[d] ^ t[a]; hi = t[d + 1] ^ t[a + 1];
  t[d] = (lo >>> 16 | hi << 16) >>> 0; t[d + 1] = (hi >>> 16 | lo << 16) >>> 0;
  fma(t, c, d);
  lo = t[b] ^ t[c]; hi = t[b + 1] ^ t[c + 1];
  t[b] = (lo << 1 | hi >>> 31) >>> 0; t[b + 1] = (hi << 1 | lo >>> 31) >>> 0;
}
// blamka runs the permutation P on the 16 words at word indexes w[0..15].
function blamka(t, w0, w1, w2, w3, w4, w5, w6, w7, w8, w9, w10, w11, w12, w13, w14, w15) {
  gb(t, w0, w4, w8, w12); gb(t, w1, w5, w9, w13); gb(t, w2, w6, w10, w14); gb(t, w3, w7, w11, w15);
  gb(t, w0, w5, w10, w15); gb(t, w1, w6, w11, w12); gb(t, w2, w7, w8, w13); gb(t, w3, w4, w9, w14);
}
// compress: out = G(in1, in2), blocks given as word offsets into their arrays.
function compress(out, oo, a1, o1, a2, o2) {
  var i, k;
  for (i = 0; i < 256; i++) R[i] = a1[o1 + i] ^ a2[o2 + i];
  for (i = 0; i < 8; i++) {
    k = i * 32;
    blamka(R, k, k + 2, k + 4, k + 6, k + 8, k + 10, k + 12, k + 14, k + 16, k + 18, k + 20, k + 22, k + 24, k + 26, k + 28, k + 30);
  }
  for (i = 0; i < 8; i++) {
    k = i * 4;
    blamka(R, k, k + 2, k + 32, k + 34, k + 64, k + 66, k + 96, k + 98, k + 128, k + 130, k + 160, k + 162, k + 192, k + 194, k + 224, k + 226);
  }
  for (i = 0; i < 256; i++) out[oo + i] = (a1[o1 + i] ^ a2[o2 + i] ^ R[i]) >>> 0;
}
// mulhi returns the high 32 bits of x * y (both uint32).
function mulhi(x, y) {
  var x0 = x & 0xffff, x1 = x >>> 16, y0 = y & 0xffff, y1 = y >>> 16;
  var p00 = x0 * y0, p01 = x0 * y1, p10 = x1 * y0;
  var mid = (p00 >>> 16) + (p01 & 0xffff) + (p10 & 0xffff);
  return (x1 * y1 + (p01 >>> 16) + (p10 >>> 16) + (mid >>> 16)) >>> 0;
}

var mem = null, memBlocks = 0, addr = new Uint32Array(256), input = new Uint32Array(256);
var salt = new TextEncoder().encode("spikes402-pow-v1");

// argon2id hashes pwd (bytes) with 1 pass, 1 lane, memKiB KiB, 32-byte tag.
function argon2id(pwd, saltBytes, memKiB) {
  var blocks = memKiB >>> 2 << 2, seg = blocks >>> 2, i, j;
  if (memBlocks !== blocks) { mem = new Uint32Array(blocks * 256); memBlocks = blocks; }
  var h0 = blake2b(concat([le32(1), le32(32), le32(memKiB), le32(1), le32(0x13), le32(2),
    le32(pwd.length), pwd, le32(saltBytes.length), saltBytes, le32(0), le32(0)]), 64);
  for (j = 0; j < 2; j++) {
    var b = hprime(concat([h0, le32(j), le32(0)]), 1024);
    for (i = 0; i < 256; i++) mem[j * 256 + i] = (b[i * 4] | b[i * 4 + 1] << 8 | b[i * 4 + 2] << 16 | b[i * 4 + 3] << 24) >>> 0;
  }
  for (var slice = 0; slice < 4; slice++) {
    var indep = slice < 2, index = slice === 0 ? 2 : 0;
    if (indep) {
      input.fill(0);
      input[4] = slice; input[6] = blocks; input[8] = 1; input[10] = 2; // pass 0, lane 0
      if (slice === 0) { input[12]++; compress(addr, 0, input, 0, Z, 0); compress(addr, 0, addr, 0, Z, 0); }
    }
    for (; index < seg; index++) {
      var cur = slice * seg + index, prev = cur - 1, rnd;
      if (indep) {
        if (index % 128 === 0) { input[12]++; compress(addr, 0, input, 0, Z, 0); compress(addr, 0, addr, 0, Z, 0); }
        rnd = addr[(index % 128) * 2];
      } else {
        rnd = mem[prev * 256];
      }
      var m = slice * seg + index - 1; // blocks it may reference (one lane)
      var p = mulhi(rnd, rnd);
      p = mulhi(p, m);
      var ref = m - (p + 1);
      compress(mem, cur * 256, mem, prev * 256, mem, ref * 256);
    }
  }
  var last = new Uint8Array(1024), o = (blocks - 1) * 256;
  for (i = 0; i < 256; i++) {
    var x = mem[o + i];
    last[i * 4] = x & 0xff; last[i * 4 + 1] = x >>> 8 & 0xff; last[i * 4 + 2] = x >>> 16 & 0xff; last[i * 4 + 3] = x >>> 24 & 0xff;
  }
  return hprime(last, 32);
}

function zeroBits(h) {
  var n = 0, i, b;
  for (i = 0; i < h.length; i++) {
    b = h[i];
    if (b !== 0) { while (!(b & 0x80)) { n++; b <<= 1; } return n; }
    n += 8;
  }
  return n;
}

self.spikes402Argon2id = function (pwd, memKiB) { return argon2id(pwd, salt, memKiB); };
self.onmessage = function (e) {
  var m = e.data, enc = new TextEncoder(), n = m.start, step = m.step || 1, attempts = 0;
  var kib = m.memoryMiB * 1024;
  for (;;) {
    attempts++;
    if (zeroBits(argon2id(enc.encode(m.challenge + ":" + n), salt, kib)) >= m.difficulty) {
      self.postMessage({ attempts: attempts, nonce: n });
      return;
    }
    self.postMessage({ attempts: attempts });
    n += step;
  }
};
})();
