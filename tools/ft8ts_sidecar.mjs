// ft8ts sidecar for SDRg35xx — block FT8 decoder in a separate process
// (ft8ts is GPL-3.0, a TypeScript port of WSJT-X v3.0.1; kept out of the
// app binary like the hamnoise/deepcw sidecars).
//
// Protocol:
//   stdin : mono float32le PCM at `rate` Hz (default 8000), continuous
//   stdout: one JSON line per decoded message:
//           {"slot":<ms>,"freq":<Hz>,"snr":<dB>,"msg":"...","kind":<n>,"ms":<decode ms>}
//           plus quiet-slot heartbeats {"slot":<ms>,"n":0,"ms":<decode ms>}
//   stderr: "READY ..." and errors
//
// Slots are aligned to the wall clock (15 s from the unix epoch, as
// FT8 transmissions are), buffered, and decoded right after each slot
// ends. The same FT8History is passed across slots so depth-3 "a7"
// decoding works like WSJT-X.
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

const slotStartFor = (ms) => Math.floor(ms / SLOT_MS) * SLOT_MS;

process.stderr.write(`READY ${rate} ${depth} ${threads}\n`);

process.stdin.on("data", (chunk) => {
  const n = Math.floor(chunk.length / 4);
  if (n <= 0) return;
  const f = new Float32Array(chunk.buffer, chunk.byteOffset, n);
  const now = Date.now();
  if (buf.length === 0) bufStartMs = now - (n / rate) * 1000;
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
  // decode every 15 s slot that has fully ended and is fully buffered
  for (;;) {
    const doneSlot = slotStartFor(Date.now()) - SLOT_MS;
    const aMs = doneSlot - bufStartMs;
    const bMs = doneSlot + SLOT_MS - bufStartMs;
    const a = Math.round((aMs / 1000) * rate);
    const b = Math.round((bMs / 1000) * rate);
    if (a < 0 || b > buf.length) break;
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
  }
});
process.stdin.on("end", () => {
  // process.exit can truncate stdout writes still in flight to the pipe
  // (the last slot's decodes would vanish); wait for the drain first.
  const bye = () => process.exit(0);
  if (process.stdout.writableNeedDrain) process.stdout.once("drain", bye);
  else setImmediate(bye);
});