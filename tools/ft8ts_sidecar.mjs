// ft8ts sidecar for SDRg35xx — block FT8 decoder in a separate process
// (ft8ts is GPL-3.0, a TypeScript port of WSJT-X v3.0.1; kept out of the
// app binary like the hamnoise/deepcw sidecars).
//
// Protocol:
//   stdin : mono float32le PCM at `rate` Hz (default 8000), continuous
//   stdout: one JSON line per decoded message:
//           {"slot":<ms>,"freq":<Hz>,"snr":<dB>,"msg":"...","kind":<n>,"ms":<decode ms>}
//           plus quiet-slot heartbeats {"slot":<ms>,"n":0,"ms":<decode ms>}
//   stderr: "READY ...", per-slot heartbeats "slot <ms> n=<n> ms=<dt>",
//           byte counter every 30 s and errors — enough to diagnose a
//           silent field failure from the device log alone.
//
// Slots are aligned to the wall clock (15 s from the unix epoch, as
// FT8 transmissions are), buffered, and decoded right after each slot
// ends. The same FT8History is passed across slots so depth-3 "a7"
// decoding works like WSJT-X.
//
// The stdin byte stream is reassembled through a small carry buffer:
// TCP chunk boundaries split float32 samples arbitrarily, and dropping
// the tail bytes of a chunk both corrupts the stream and makes
// Float32Array(chunk.buffer, chunk.byteOffset) throw when the offset
// is unaligned.
//
// usage: node ft8ts_sidecar.mjs lib.mjs rate depth threads low high
const { decodeFT8, FT8History } = await import(process.argv[2]);
const [, , , rateS, depthS, threadsS, lowS, highS] = process.argv;
const rate = parseInt(rateS || "8000", 10);
const depth = Math.min(3, Math.max(1, parseInt(depthS || "2", 10)));
const threads = Math.min(4, Math.max(1, parseInt(threadsS || "1", 10)));
const freqLow = parseInt(lowS || "200", 10);
const freqHigh = Math.min(parseInt(highS || "3000", 10), rate / 2 - 200);

const SLOT_MS = 15000;
const slotSamples = Math.round((SLOT_MS / 1000) * rate);
const history = new FT8History();

let buf = new Float32Array(0);
let bufStartMs = 0; // wall-clock ms of buf[0]
let carry = Buffer.alloc(0); // stdin bytes awaiting a full float32
let bytesIn = 0;

const slotStartFor = (ms) => Math.floor(ms / SLOT_MS) * SLOT_MS;

setInterval(() => {
  process.stderr.write(`stream ${(bytesIn / 1e6).toFixed(1)}MB buffered=${(buf.length / rate).toFixed(1)}s\n`);
}, 30000).unref();

process.stderr.write(`READY ${rate} ${depth} ${threads}\n`);

const decodeSlot = (doneSlot) => {
  const aMs = doneSlot - bufStartMs;
  const bMs = doneSlot + SLOT_MS - bufStartMs;
  const a = Math.round((aMs / 1000) * rate);
  const b = Math.round((bMs / 1000) * rate);
  if (a < 0 || b > buf.length) return false;
  const samples = buf.slice(a, b);
  buf = buf.slice(b);
  bufStartMs += (b / rate) * 1000;
  const t0 = Date.now();
  let out = [];
  try {
    out = decodeFT8(samples, {
      sampleRate: rate,
      freqLow,
      freqHigh,
      depth,
      history,
      slotStart: doneSlot,
    });
  } catch (e) {
    process.stderr.write(`decode error: ${e}\n`);
  }
  const dt = Date.now() - t0;
  for (const d of out) {
    process.stdout.write(
      JSON.stringify({
        slot: doneSlot,
        freq: d.freq,
        snr: d.snr,
        msg: d.msg,
        kind: d.kind ?? 0,
        ms: dt,
      }) + "\n"
    );
  }
  if (out.length === 0) {
    process.stdout.write(JSON.stringify({ slot: doneSlot, n: 0, ms: dt }) + "\n");
  }
  process.stderr.write(`slot ${doneSlot} n=${out.length} ms=${dt}\n`);
  return true;
};

process.stdin.on("data", (chunk) => {
  try {
    bytesIn += chunk.length;
    carry = carry.length ? Buffer.concat([carry, chunk]) : chunk;
    const n = Math.floor(carry.length / 4);
    if (n <= 0) return;
    // copy into a fresh aligned ArrayBuffer — the chunk's own buffer
    // can be unaligned and Float32Array would throw on it
    const f = new Float32Array(carry.buffer.slice(carry.byteOffset, carry.byteOffset + n * 4));
    carry = carry.subarray(n * 4);
    const now = Date.now();
    if (buf.length === 0) bufStartMs = now - (n / rate) * 1000;

    // Audio-end vs wall clock: a gap (reconnect, chain rebuild, radio
    // stall) leaves bufStartMs anchored before the hole and the needed
    // slots never become coverable. Resync to the live edge instead.
    const lagMs = now - (bufStartMs + (buf.length / rate) * 1000);
    if (lagMs > 2000 && buf.length > 0) {
      process.stderr.write(`gap ${((lagMs - (n / rate) * 1000) / 1000).toFixed(1)}s — resync`+String.fromCharCode(10));
      buf = new Float32Array(0);
      bufStartMs = now - (n / rate) * 1000;
    }
    const merged = new Float32Array(buf.length + n);
    merged.set(buf, 0);
    merged.set(f, buf.length);
    buf = merged;
    const maxKeep = slotSamples * 2;
    if (buf.length > maxKeep) {
      const drop = buf.length - maxKeep;
      buf = buf.slice(drop);
      bufStartMs += (drop / rate) * 1000;
    }
    // decode every 15 s slot that has fully ended and is fully
    // buffered (during fill-up doneSlot predating the buffer start is
    // normal — waiting; real holes were resynced above)
    for (;;) {
      const doneSlot = slotStartFor(Date.now()) - SLOT_MS;
      if (!decodeSlot(doneSlot)) break;
    }
  } catch (e) {
    process.stderr.write(`stdin error: ${e}\n`);
  }
});
process.stdin.on("end", () => {
  // process.exit can truncate stdout writes still in flight to the pipe
  // (the last slot's decodes would vanish); wait for the drain first.
  const bye = () => process.exit(0);
  if (process.stdout.writableNeedDrain) process.stdout.once("drain", bye);
  else setImmediate(bye);
});