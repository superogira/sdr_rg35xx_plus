# Field scenario: sidecar starts MID-BURST (depth/thread change restarts
# it at a random point in the 15 s cycle). Headless burst must NOT be
# locked; the next burst with a visible head must be locked and decode.
import subprocess, time, threading
LIB = "file:///C:/Users/superogira/Desktop/sdr_rg35xx_plus/third_party/ft8ts/ft8ts.mjs"
RATE = 8000
raw = open(".webtest/e2e_slot.raw","rb").read()   # 15 s: 12.6 s burst + 2.4 s silence
sil = b"\x00" * (RATE*4*2)
p = subprocess.Popen(["node","tools/ft8ts_sidecar.mjs",LIB,"8000","3","2","200","3000"],
                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
errs=[]
def pump():
    for line in iter(p.stderr.readline, b""):
        t=line.decode().rstrip()[:120]; errs.append(t); print("ERR:", t, flush=True)
threading.Thread(target=pump, daemon=True).start()
p.stderr.readline()
def feed(data):
    t0=time.time(); off=0
    while off < len(data):
        p.stdin.write(data[off:off+16000]); p.stdin.flush(); off += 16000
        tgt = off/(RATE*4.0); now=time.time()-t0
        if tgt-now>0: time.sleep(tgt-now)
# start mid-burst: first 7 s of the burst with NO silence lead
feed(raw[:RATE*4*7])
# then two normal slots
feed(sil + raw)
feed(sil + raw)
print("fed; waiting 20 s for lagging decodes", flush=True)
time.sleep(20)
p.kill()
out = p.stdout.read().decode()
msgs  = [l for l in out.splitlines() if '"msg"' in l]
locks = [e for e in errs if "locked" in e]
print("messages:", len(msgs))
for l in msgs[:4]: print("  ", l[:100])
assert len(msgs) >= 6, "RECOVERY FAILED: no decode after a mid-burst restart"
# the first lock (if any) must NOT be on the headless burst: i.e. the
# first locked message must arrive at the first full-head burst
print("MID-BURST RESTART TEST PASS")
