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
// Slot timing is found by DECODE-PROBING, not an envelope: an RMS
// envelope cannot separate an FT8 burst from noise below ~+12 dB SNR
// (burst/gap ratio is 1.05 at -10 dB), so the previous envelope lock
// never locked on real-world bands. Instead, once a full 15 s window
// is buffered, decode it at a set of candidate offsets (0 s and
// ±0.5/±1 s; decodeFT8's own sync search covers ±2 s from the block
// start). A successful decode defines the grid: that window's start is
// the slot boundary. Later windows advance by exactly 15 s; small
// drift is re-centred after every successful decode (the decode's own
// timing estimate does not expose dt, so the grid follows the audio by
// construction of decode-at-grid). A quiet run (no decodes) re-probes
// at the offsets again, so the lock survives silence and sample drops.
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
const history = new FT8History();
const pool = threads > 1 ? new FT8DecoderPool({ threads }) : null;

// probe offsets: decodeFT8 sync search covers ±2 s around the window
// start, so these 5 offsets cover a full slot with margin; 0 is tried
// first (the common case: wall-clock-ish grid or already-drifted)
const PROBES = [0, 0.5, 1.0, -0.5, -1.0].map((s) => Math.round(s * rate));

let buf = new Float32Array(0);
let carry = Buffer.alloc(0); // stdin bytes awaiting a full float32
let bytesIn = 0;
let grid = -1; // buffer index of the next slot window start; -1 = not locked
let slotLabel = 0; // wall-clock ms label of the window at `grid`
let zeroRun = 0; // consecutive empty windows since last decode
let probing = false;

const stderr = (m) => process.stderr.write(m + "\n");

setInterval(() => {
  stderr(
    `stream ${(bytesIn / 1e6).toFixed(1)}MB buffered=${(buf.length / rate).toFixed(1)}s locked=${grid >= 0}`
  );
}, 30000).unref();

stderr(`READY ${rate} ${depth} ${threads}`);

async function decodeAt(idx, label, register) {
  const samples = buf.slice(idx, idx + SLOT);
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
  const dt = Date.now() - t0;
  for (const d of out) {
    process.stdout.write(
      JSON.stringify({ slot: label, freq: d.freq, snr: d.snr, msg: d.msg, kind: d.kind ?? 0, ms: dt }) + "\n"
    );
  }
  if (register && out.length === 0) {
    process.stdout.write(JSON.stringify({ slot: label, n: 0, ms: dt }) + "\n");
  }
  stderr(`slot ${label} n=${out.length} ms=${dt}`);
  return out.length;
}

// The wall-clock label of a window starting at buffer index idx: the
// newest sample arrived ~now, so the window's start was
// (buf.length - idx)/rate seconds ago.
function labelFor(idx) {
  const startWall = Date.now() - ((buf.length - idx) / rate) * 1000;
  return Math.floor(startWall / SLOT_MS) * SLOT_MS;
}

// Serialised pump: pool.decode() awaits, and awaiting inside the data
// handler lets a second chunk's handler run to completion meanwhile —
// concurrent decodes then advance `grid` from multiple places. Chunks
// that arrive while a decode is in flight are queued and processed
// strictly one at a time.
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

    if (grid < 0) {
      // UNLOCKED: probe a full 15 s window for a transmission. Probe
      // the newest complete window first (least latency), then older
      // ones, at each of the candidate offsets. A decode at offset o
      // of a window starting at w means the slot boundary is w+o —
      // except that sync search tolerates ±2 s, so w+o is the boundary
      // only when |o| < 2; the offsets here are all within ±1 s, and
      // the NEXT window starts 15 s later from that boundary.
      if (!probing && buf.length >= SLOT) {
        probing = true;
        try {
          // Dense boundary scan: the transmission heads land at arbitrary
          // phases inside the buffer, so SLOT-aligned windows miss them
          // (mid-burst restart test: heads at 9 s and 24 s of 45 s never
          // aligned with windows [0,15],[15,30],[30,45)). decodeFT8's own
          // sync search covers ±2 s, so stepping the candidate boundary
          // every 3 s is enough to find any phase; scan the newest two
          // slots worth of candidates.
          const step = Math.round(3 * rate);
          outer: for (let idx = buf.length - SLOT; idx >= 0; idx -= step) {
            const got = await decodeAt(idx, labelFor(idx), false);
            if (got > 0) {
              // this window's start is (close to) a slot boundary: the
              // NEXT window starts one slot later
              grid = idx + SLOT;
              slotLabel = labelFor(idx) + SLOT_MS;
              zeroRun = 0;
              stderr(`locked: grid at ${idx}, next slot ${slotLabel}`);
              break outer;
            }
          }
        } finally {
          probing = false;
        }
        // no luck: bound the buffer to ~2 slots and try again with
        // fresh audio (a quiet band or sub-threshold signals)
        if (grid < 0 && buf.length > SLOT * 2) {
          const drop = buf.length - SLOT * 2;
          buf = buf.slice(drop);
        }
      }
    } else {
      // LOCKED: decode consecutive windows from the grid
      let guard = 0;
      while (grid >= 0 && grid + SLOT <= buf.length && guard++ < 4) {
        const got = await decodeAt(grid, slotLabel, true);
        zeroRun = got > 0 ? 0 : zeroRun + 1;
        grid += SLOT;
        slotLabel += SLOT_MS;
      }
      // Quiet run: the band went silent or the grid drifted (dropped
      // audio shifts the stream). 2+ empties re-probe; a probe hit
      // both re-locks and catches up on the missed windows.
      if (zeroRun >= 2 && !probing && buf.length >= SLOT) {
        probing = true;
        try {
          let best = -1;
          for (let w = buf.length - SLOT; w >= 0 && w >= buf.length - SLOT * 2; w -= Math.round(0.5 * rate)) {
            for (const off of PROBES) {
              const idx = w - off;
              if (idx < 0 || idx + SLOT > buf.length) continue;
              const got = await decodeAt(idx, labelFor(idx), false);
              if (got > 0) {
                best = idx;
                break;
              }
            }
            if (best >= 0) break;
          }
          if (best >= 0) {
            grid = best + SLOT;
            slotLabel = labelFor(best) + SLOT_MS;
            zeroRun = 0;
            stderr(`re-locked at ${best}, next slot ${slotLabel}`);
          } else if (zeroRun >= 6) {
            grid = -1; // genuinely silent: back to fresh probing
            stderr("unlocked (long silence)");
          }
        } finally {
          probing = false;
        }
      }
      // bound memory: keep the pending window plus context
      if (grid >= 0 && buf.length - grid > SLOT + Math.round(2 * rate)) {
        const drop = buf.length - grid - SLOT - Math.round(2 * rate);
        buf = buf.slice(drop);
        grid -= drop;
      } else if (grid < 0 && buf.length > SLOT * 2) {
        const drop = buf.length - SLOT * 2;
        buf = buf.slice(drop);
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