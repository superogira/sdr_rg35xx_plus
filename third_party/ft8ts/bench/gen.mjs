// Generate one 15 s FT8 slot of float32le mono at `rate` Hz carrying
// `count` messages at `snrDb`, using ft8ts's own encoder (so the bench
// signal is exactly what the decoder expects).
// usage: node gen.mjs lib.mjs out.raw rate count snrDb seed
const { encodeFT8 } = await import(process.argv[2]);
import { writeFileSync } from "node:fs";
const [, , , out, rateS, countS, snrS, seedS] = process.argv;
const rate = parseInt(rateS, 10);
const count = parseInt(countS, 10);
const snrDb = parseFloat(snrS);
let seed = parseInt(seedS || "7", 10);
const rnd = () => {
  seed = (seed * 1103515245 + 12345) & 0x7fffffff;
  return seed / 0x7fffffff;
};
const n = rate * 15;
const mix = new Float64Array(n);
// ft8ts's encoder emits 79 symbols x 1920 samples = 151680 REGARDLESS
// of sampleRate (fixed 12 kHz timing), so encode at 12k and resample
// into the slot ourselves.
const ENCRATE = 12000;
const msgs = [
  "CQ HS0ABC OK03", "CQ DL8YHR JO41", "HS0ABC DL8YHR -10",
  "DL8YHR HS0ABC R-14", "HS0ABC DL8YHR RR73", "DL8YHR HS0ABC 73",
  "CQ JA1XYZ PM95", "CQ VK3ABC QF22", "CQ EA2BFM IN83", "CQ F5BZB JN05",
];
let peak = 0;
for (let i = 0; i < count; i++) {
  const s = encodeFT8(msgs[i % msgs.length], { sampleRate: ENCRATE, baseFrequency: 400 + i * 240 });
  for (let k = 0; k < n; k++) {
    const p = (k * ENCRATE) / rate;
    const i0 = Math.floor(p);
    if (i0 + 1 >= s.length) break;
    const f = p - i0;
    mix[k] += s[i0] * (1 - f) + s[i0 + 1] * f;
    if (Math.abs(mix[k]) > peak) peak = Math.abs(mix[k]);
  }
}
// signal rms
let srms = 0;
for (let k = 0; k < n; k++) srms += mix[k] * mix[k];
srms = Math.sqrt(srms / n);
// noise for target SNR (dB): noise rms = srms / 10^(snr/20)
const nrms = srms / Math.pow(10, snrDb / 20);
const out32 = new Float32Array(n);
for (let k = 0; k < n; k++) {
  // gaussian via sum of uniforms
  const g = (rnd() + rnd() + rnd() + rnd() - 2) * 1.732;
  out32[k] = mix[k] + g * nrms;
}
writeFileSync(out, Buffer.from(out32.buffer));
console.log(`wrote ${out}: ${count} msgs @ ${snrDb} dB, peak ${peak.toFixed(3)}`);
