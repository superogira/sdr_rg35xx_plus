// Decode one raw f32 slot and report wall time + RSS.
// usage: node dec.mjs lib.mjs in.raw rate depth threads mode
const { decodeFT8, decodeFT4, FT8DecoderPool } = await import(process.argv[2]);
import { readFileSync } from "node:fs";
const [, , , inPath, rateS, depthS, threadsS, mode] = process.argv;
const rate = parseInt(rateS, 10);
const depth = parseInt(depthS, 10);
const threads = parseInt(threadsS || "1", 10);
const raw = readFileSync(inPath);
const samples = new Float32Array(raw.buffer, raw.byteOffset, raw.length / 4);
const opts = {
  sampleRate: rate,
  depth,
  freqLow: 200,
  freqHigh: Math.min(3000, rate / 2 - 200),
};
const t0 = Date.now();
let out;
if (threads > 1) {
  const pool = new FT8DecoderPool({ threads });
  out = await pool.decode(samples, opts);
  pool.terminate();
} else {
  out = (mode === "ft4" ? decodeFT4 : decodeFT8)(samples, opts);
}
const dt = Date.now() - t0;
const rss = process.memoryUsage().rss / 1048576;
console.log(`depth=${depth} threads=${threads} mode=${mode} time=${dt}ms rss=${rss.toFixed(0)}MB n=${out.length}`);
for (const d of out) console.log(`  ${d.freq}Hz ${d.snr.toFixed(1)}dB ${d.msg}`);