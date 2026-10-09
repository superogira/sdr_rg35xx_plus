// ft8ts sidecar for SDRg35xx — block FT8 decoder in a separate process
// (ft8ts is GPL-3.0, a TypeScript port of WSJT-X v3.0.1; kept out of the
// app binary like the hamnoise/deepcw sidecars).
//
// Protocol:
//   stdin : mono float32le PCM at `rate` Hz (default 8000), continuous
//   stdout: one JSON line per decoded message:
//           {"slot":<ms>,"freq":<Hz>,"snr":<dB>,"msg":"...","kind":<n>,"ms":<decode ms>}
//           plus quiet-slot heartbeats {"slot":<ms>,"n":0,"ms":<decode ms>}
//   stderr: "READY ...", per-slot heartbeats, byte counter every 30 s,
//           lock/resync events and errors — enough to diagnose a
//           silent field failure from the device log alone.
//
// Slot timing is PHASE-LOCKED TO THE ARRIVING AUDIO, not to the wall
// clock: decodeFT8's sync search only covers bursts within ~2 s of the
// block start (measured), while a networked source (WAN rtl_tcp)
// delivers audio seconds late and with jitter — wall-clock slicing
// decoded nothing in the field. The lock finds a 12.6 s transmission
// burst in the envelope (0.5 s RMS blocks), cuts the window 0.1 s
// before it, then advances by exactly 15 s per slot. Wall-clock slot
// labels are derived once at lock time (for history/a7) and advance
// with the grid. Three consecutive empty windows re-run the burst
// search in case the stream slipped.
//
// usage: node ft8ts_sidecar.mjs lib.mjs rate depth threads low high
const { decodeFT8, FT8DecoderPool, FT8History } = await import(process.argv[2]);
const [, , , rateS, depthS, threadsS, lowS, highS] = process.argv;
const rate = parseInt(rateS || "8000", 10);
const depth = Math.min(3, Math.max(1, parseInt(depthS || "2", 10)));
const threads = Math.min(4, Math.max(1, parseInt(threadsS || "1", 10)));
const freqLow = parseInt(lowS || "200", 10);
const freqHigh = Math.min(parseInt(highS || "3000", 10), rate / 2 - 200);

const SLOT_MS = 15000;
const SLOT = Math.round((SLOT_MS / 1000) * rate); // samples per slot
const BLK = Math.round(0.5 * rate); // envelope block: 0.5 s
const BURST_BLOCKS = 16; // >= 8 s of tone = a transmission
const PRE = Math.round(0.1 * rate); // window lead before the burst
const history = new FT8History();
const pool = threads > 1 ? new FT8DecoderPool({ threads }) : null;

let buf = new Float32Array(0);
let carry = Buffer.alloc(0); // stdin bytes awaiting a full float32
let bytesIn = 0;
let locked = false;
let grid = 0; // buffer index of the next slot window start
let slotLabel = 0; // wall-clock ms label of that slot
let zeroRun = 0; // consecutive empty decodes (re-lock trigger)
let envPos = 0; // buffer index just past the last env block
let envStart = 0; // buffer index of env[0]
const env = []; // RMS per 0.5 s block from envStart

const stderr = (m) => process.stderr.write(m + "\n");

setInterval(() => {
  stderr(`stream ${(bytesIn / 1e6).toFixed(1)}MB buffered=${(buf.length / rate).toFixed(1)}s locked=${locked}`);
}, 30000).unref();

stderr(`READY ${rate} ${depth} ${threads}`);

// envelope lock: start block of the longest tone run, or -1
function findBurst() {
  if (env.length < BURST_BLOCKS + 8) return -1;
  let max = 0;
  for (const v of env) if (v > max) max = v;
  if (max <= 0) return -1;
  const thr = max * 0.25;
  let best = -1, bestLen = 0, start = -1, len = 0;
  for (let i = 0; i < env.length; i++) {
    if (env[i] > thr) {
      if (start < 0) start = i;
      len = i - start + 1;
      if (len > bestLen) { bestLen = len; best = start; }
    } else {
      start = -1;
    }
  }
  return bestLen >= BURST_BLOCKS ? best : -1;
}

// extend env over whole blocks; drop blocks that fell behind `grid`
function updateEnv() {
  while (envPos + BLK <= buf.length) {
    let e = 0;
    for (let i = 0; i < BLK; i++) {
      const v = buf[envPos + i];
      e += v * v;
    }
    env.push(Math.sqrt(e / BLK));
    envPos += BLK;
  }
  while (envStart < grid && env.length > 0) {
    env.shift();
    envStart += BLK;
  }
}

// env[0] always corresponds to buffer index `grid` (updateEnv trims it)
function lockAt(burstBlock) {
  const burstIdx = grid + burstBlock * BLK;
  const burstWall = Date.now() - ((buf.length - burstIdx) / rate) * 1000;
  grid = Math.max(0, burstIdx - PRE);
  slotLabel = Math.floor(burstWall / SLOT_MS) * SLOT_MS;
  locked = true;
  zeroRun = 0;
  stderr(`locked: burst ${((buf.length - burstIdx) / rate).toFixed(1)}s old, slot ${slotLabel}`);
}

async function decodeWindow() {
  if (grid + SLOT > buf.length) return false;
  const samples = buf.slice(grid, grid + SLOT);
  const label = slotLabel;
  const keepFrom = Math.max(0, grid + SLOT - BLK); // 0.5 s context lead
  buf = buf.slice(keepFrom);
  grid -= keepFrom;
  envPos -= keepFrom;
  envStart -= keepFrom;
  const t0 = Date.now();
  let out = [];
  try {
    out = pool
      ? await pool.decode(samples, {
          sampleRate: rate,
          freqLow,
          freqHigh,
          depth,
          history,
          slotStart: label,
        })
      : decodeFT8(samples, {
          sampleRate: rate,
          freqLow,
          freqHigh,
          depth,
          history,
          slotStart: label,
        });
  } catch (e) {
    stderr(`decode error: ${e}`);
  }
  let wrms = 0;
  for (let i = 0; i < samples.length; i++) wrms += samples[i] * samples[i];
  wrms = Math.sqrt(wrms / Math.max(1, samples.length));
  const dt = Date.now() - t0;
  for (const d of out) {
    process.stdout.write(
      JSON.stringify({ slot: label, freq: d.freq, snr: d.snr, msg: d.msg, kind: d.kind ?? 0, ms: dt }) + "\n"
    );
  }
  if (out.length === 0) {
    process.stdout.write(JSON.stringify({ slot: label, n: 0, ms: dt }) + "\n");
  }
  stderr(`slot ${label} n=${out.length} ms=${dt} rms=${wrms.toFixed(3)}`);
  zeroRun = out.length === 0 ? zeroRun + 1 : 0;
  grid += SLOT;
  slotLabel += SLOT_MS;
  return true;
}

// Serialised pump: pool.decode() awaits, and awaiting inside the data
// handler lets a second chunk's handler run to completion meanwhile —
// concurrent decodes then advance `grid` from multiple places (field
// log: 159 slots with ms up to 77 s, one slot label repeated 40x, zero
// decodes). Chunks that arrive while a decode is in flight are queued
// and processed strictly one at a time.
const pending = [];
let pumping = false;

async function pump() {
  if (pumping) return;
  pumping = true;
  try {
    while (pending.length > 0) {
      const chunk = pending.shift();
      await ingest(chunk);
    }
  } finally {
    pumping = false;
  }
}

async function ingest(chunk) {
  try {
    bytesIn += chunk.length;
    carry = carry.length ? Buffer.concat([carry, chunk]) : chunk;
    const n = Math.floor(carry.length / 4);
    if (n <= 0) return;
    const f = new Float32Array(carry.buffer.slice(carry.byteOffset, carry.byteOffset + n * 4));
    carry = carry.subarray(n * 4);
    const merged = new Float32Array(buf.length + n);
    merged.set(buf, 0);
    merged.set(f, buf.length);
    buf = merged;

    if (!locked) {
      updateEnv();
      const b = findBurst();
      if (b >= 0) lockAt(b);
      else if (buf.length > SLOT * 3) {
        // no FT8 burst in three slots of audio: drop the old tail and
        // rebuild the envelope so a later burst is still findable
        const drop = buf.length - SLOT;
        buf = buf.slice(drop);
        env.length = 0;
        envPos = 0;
        envStart = 0;
        updateEnv();
      }
    }
    if (locked) {
      updateEnv();
      let guard = 0;
      while (grid + SLOT <= buf.length && guard++ < 8) await decodeWindow();
      if (zeroRun >= 2) {
        // Two empty windows: either a genuinely quiet stretch or the
        // grid slipped (jitter accumulation, dropped audio). A burst
        // visible far from the next window start proves a slip —
        // re-lock; silence keeps the grid (quiet slots are normal).
        const b = findBurst();
        if (b >= 0) {
          const burstIdx = grid + b * BLK;
          const offSec = (burstIdx - PRE - grid) / rate;
          if (offSec > 2.5 || offSec < -1) {
            stderr(`slip ${(offSec).toFixed(1)}s — re-lock`);
            lockAt(b);
          }
        } else if (zeroRun >= 6) {
          locked = false; // long silence: search from scratch
          stderr("unlocked (long silence)");
        }
      }
      const maxKeep = SLOT * 3;
      if (buf.length > maxKeep) {
        const drop = buf.length - maxKeep;
        buf = buf.slice(drop);
        grid = Math.max(0, grid - drop);
        envPos -= drop;
        envStart -= drop;
      }
    }
  } catch (e) {
    stderr(`stdin error: ${e}`);
  }
}

process.stdin.on("data", (chunk) => {
  pending.push(chunk);
  pump();
});
process.stdin.on("end", () => {
  // process.exit can truncate stdout writes still in flight to the pipe
  // (the last slot's decodes would vanish); wait for the drain first.
  const bye = () => process.exit(0);
  if (process.stdout.writableNeedDrain) process.stdout.once("drain", bye);
  else setImmediate(bye);
});