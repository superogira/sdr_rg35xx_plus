// deepcw — streaming Morse decoder sidecar for SDRg35xx.
//
// Wraps the DeepCW ONNX model (github.com/e04/deepcw-engine,
// AGPL-3.0-only) behind a tiny pipe protocol so the AGPL code and model
// never link into the main application binary (same isolation pattern
// as the hamnoise and rtl_tcp sidecars).
//
// Protocol:
//   stdin : mono s16 PCM at 3200 Hz, any chunk size
//   stdout: "READY <threads> <window_samples>" then one line per
//           decode window with the characters this window owns
//
// Streaming design (measured on the RG35XX): the model needs ~5 s of
// context — shorter windows decode garbage — so windows are fixed at
// `window_s` with a `margin_s` trailing overlap (stride = window -
// margin). Seams are made clean on the CTC TIME axis: a character is
// emitted only if its best-path run STARTS before the stride boundary,
// and a character already in progress at frame 0 belongs to the
// previous window (which saw it with context on both sides).
//
// Usage: deepcw model.onnx model.onnx.json [threads] [window_s] [margin_s]
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "onnxruntime_c_api.h"

#define RATE 3200
#define FFT_LEN 256
#define HOP_LEN 48
#define BIN_LO 32
#define NBINS 65
#define NCLASS 42
#define BLANK 41
#define EDGE_FRAMES 48 // ~0.72 s of unreliable left border

static const char CHARS[] = ",./0123456789?ABCDEFGHIJKLMNOPQRSTUVWXYZ ";

static const OrtApi *ort;
static OrtEnv *env;
static OrtSession *sess;
static OrtSessionOptions *opts;
static const OrtMemoryInfo *meminfo;
static char *inname, *outname;

static int16_t *buf; // linear sample buffer holding [bufbase, bufbase+bufn)
static long bufbase;
static int bufn, bufcap;

static void die(const char *what, OrtStatus *st) {
    fprintf(stderr, "deepcw: %s: %s\n", what, st ? ort->GetErrorMessage(st) : "error");
    exit(1);
}

static void buf_push(const int16_t *p, int n) {
    if (bufn + n > bufcap) {
        bufcap = bufn + n + 8192;
        buf = realloc(buf, bufcap * sizeof(int16_t));
    }
    memcpy(buf + bufn, p, n * sizeof(int16_t));
    bufn += n;
}

static void buf_read(int off, int len, float *out) {
    for (int i = 0; i < len; i++) out[i] = buf[off + i] / 32768.0f;
}

static void fft(float *re, float *im, int n) {
    for (int i = 1, j = 0; i < n; i++) {
        int bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) {
            float t = re[i]; re[i] = re[j]; re[j] = t;
            t = im[i]; im[i] = im[j]; im[j] = t;
        }
    }
    for (int len = 2; len <= n; len <<= 1) {
        double ang = -2 * M_PI / len;
        float wr = (float)cos(ang), wi = (float)sin(ang);
        for (int i = 0; i < n; i += len) {
            float cr = 1, ci = 0;
            for (int k = 0; k < len / 2; k++) {
                float ur = re[i + k], ui = im[i + k];
                float vr = re[i + k + len / 2] * cr - im[i + k + len / 2] * ci;
                float vi = re[i + k + len / 2] * ci + im[i + k + len / 2] * cr;
                re[i + k] = ur + vr; im[i + k] = ui + vi;
                re[i + k + len / 2] = ur - vr; im[i + k + len / 2] = ui - vi;
                float nr = cr * wr - ci * wi;
                ci = cr * wi + ci * wr;
                cr = nr;
            }
        }
    }
}

static float win256[FFT_LEN];

static float *spectrogram(int base, int len, int *frames) {
    int pad = FFT_LEN / 2;
    int need = len + 2 * pad;
    float *p = malloc(need * sizeof(float));
    for (int i = 0; i < pad; i++) {
        int src = pad - i;
        if (src > len - 1) src = len - 1;
        buf_read(base + src, 1, &p[i]);
    }
    buf_read(base, len, p + pad);
    for (int i = 0; i < pad; i++) {
        int src = len - 1 - i;
        if (src < 0) src = 0;
        buf_read(base + src, 1, &p[pad + len + i]);
    }
    int fr = 1 + (need - FFT_LEN) / HOP_LEN;
    float *spec = malloc((size_t)fr * NBINS * sizeof(float));
    float re[FFT_LEN], im[FFT_LEN];
    for (int f = 0; f < fr; f++) {
        for (int k = 0; k < FFT_LEN; k++) {
            re[k] = p[f * HOP_LEN + k] * win256[k];
            im[k] = 0;
        }
        fft(re, im, FFT_LEN);
        for (int b = 0; b < NBINS; b++) {
            int bin = BIN_LO + b;
            float m = sqrtf(re[bin] * re[bin] + im[bin] * im[bin]);
            spec[(size_t)f * NBINS + b] = log1pf(m);
        }
    }
    free(p);
    *frames = fr;
    return spec;
}

// Run one window; emit the characters this window owns (see header).
// pre_len: samples kept in the ring immediately before `base`; their
// energy tells whether a tone was already in progress at the window
// start (then the previous window emitted it — skip) or the window
// opens on silence (a character starting at frame 0 is ours).
static void decode_window(int base, int len, int stride_frames, int first, int last,
                          FILE *outf) {
    int fr = 0;
#ifdef DEBUG
    {
        float a6[6]; buf_read(base, 6, a6);
        fprintf(stderr, "DBGWIN base_rel=%d len=%d a0=%.4f %.4f %.4f%c", base, len, a6[0], a6[1], a6[2], 10);
    }
#endif
    float *spec = spectrogram(base, len, &fr);
#ifdef DEBUG
    static int dumped = 0;
    if (!dumped) {
        dumped = 1;
        FILE *df = fopen("/tmp/deepcw/spec_sidecar.bin", "wb");
        if (df) { fwrite(spec, 4, (size_t)fr * NBINS, df); fclose(df); }
    }
#endif
    int64_t dims[4] = {1, 1, fr, NBINS};
    OrtValue *in = NULL, *outv = NULL;
    OrtStatus *st = ort->CreateTensorWithDataAsOrtValue(
        meminfo, spec, (size_t)fr * NBINS * 4, dims, 4,
        ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT, &in);
    if (st) die("tensor", st);
    const char *ins[1] = {inname};
    const char *outs[1] = {outname};
    st = ort->Run(sess, NULL, ins, (const OrtValue *const *)&in, 1, outs, 1, &outv);
    if (st) die("run", st);
    float *lp = NULL;
    ort->GetTensorMutableData(outv, (void **)&lp);
    OrtTensorTypeAndShapeInfo *ti = NULL;
    ort->GetTensorTypeAndShape(outv, &ti);
    int64_t od[3];
    ort->GetDimensions(ti, od, 3);
    ort->ReleaseTensorTypeAndShapeInfo(ti);
    int T = (int)od[1];
    // Ownership by absolute CTC frame: this window emits runs starting
    // in [S + edge, S + stride + edge) where S = s_frames. The ranges
    // tile the stream, so nothing is lost or duplicated; `edge` skips
    // the model's unreliable left border (the previous window covers
    // it with full context). First window: edge = 0. Last: to the end.
    int edge = first ? 0 : EDGE_FRAMES;
    int hi = last ? T : stride_frames + EDGE_FRAMES;
    char text[512];
    int tlen = 0;
    int prev = -1;
    int run_start = 0;
    for (int t = 0; t <= T; t++) {
        int cur = BLANK;
        if (t < T) {
            float bv = lp[(size_t)t * NCLASS];
            for (int c2 = 1; c2 < NCLASS; c2++)
                if (lp[(size_t)t * NCLASS + c2] > bv) { bv = lp[(size_t)t * NCLASS + c2]; cur = c2; }
        }
        if (cur != prev) {
            if (prev != BLANK && prev != -1) {
                if (run_start >= edge && run_start < hi && tlen + 1 < (int)sizeof(text))
                    text[tlen++] = CHARS[prev];
            }
            run_start = t;
            prev = cur;
        }
    }
    text[tlen] = 0;
#ifdef DEBUG
    {
        FILE *df = fopen("/tmp/deepcw/path_sidecar.txt", "a");
        if (df) {
            int prev2 = -1;
            for (int t = 0; t < T; t++) {
                int cur = BLANK; float bv = lp[(size_t)t * NCLASS];
                for (int c3 = 1; c3 < NCLASS; c3++) if (lp[(size_t)t * NCLASS + c3] > bv) { bv = lp[(size_t)t * NCLASS + c3]; cur = c3; }
                if (cur != prev2) { fprintf(df, "%d:%d ", t, cur); prev2 = cur; }
            }
            fprintf(df, "| emit=[%s]%c", text, 10);
            fclose(df);
        }
    }
#endif
    if (tlen) {
        fputs(text, outf);
        fputc('\n', outf);
        fflush(outf);
    }
    free(spec);
    ort->ReleaseValue(in);
    ort->ReleaseValue(outv);
}

int main(int argc, char **argv) {
    if (argc < 3) {
        fprintf(stderr, "usage: deepcw model.onnx model.onnx.json [threads] [window_s] [margin_s]\n");
        return 2;
    }
    int threads = argc > 3 ? atoi(argv[3]) : 1;
    double window_s = argc > 4 ? atof(argv[4]) : 5.0;
    double margin_s = argc > 5 ? atof(argv[5]) : 1.0;
    if (threads < 1) threads = 1;
    if (threads > 4) threads = 4;
    if (window_s < 3) window_s = 3;
    if (window_s > 20) window_s = 20;
    if (margin_s < 0.5) margin_s = 0.5;
    if (margin_s > window_s / 2) margin_s = window_s / 2;

    int wins = (int)(window_s * RATE);
    int stride = (int)((window_s - margin_s) * RATE);
    if (stride < RATE) stride = RATE;
    int stride_frames = stride / HOP_LEN;

    for (int i = 0; i < FFT_LEN; i++)
        win256[i] = (float)(0.5 - 0.5 * cos(2 * M_PI * i / FFT_LEN));

    bufcap = wins + 4 * RATE;
    buf = malloc(bufcap * sizeof(int16_t));
    bufn = 0;
    bufbase = 0;

    ort = OrtGetApiBase()->GetApi(ORT_API_VERSION);
    if (ort->CreateEnv(ORT_LOGGING_LEVEL_ERROR, "deepcw", &env)) die("env", NULL);
    if (ort->CreateSessionOptions(&opts)) die("opts", NULL);
    ort->SetIntraOpNumThreads(opts, threads);
    ort->SetSessionGraphOptimizationLevel(opts, ORT_ENABLE_ALL);
    OrtStatus *st = ort->CreateSession(env, argv[1], opts, &sess);
    if (st) die("session", st);
    OrtAllocator *alloc = NULL;
    ort->GetAllocatorWithDefaultOptions(&alloc);
    ort->SessionGetInputName(sess, 0, alloc, &inname);
    ort->SessionGetOutputName(sess, 0, alloc, &outname);
    ort->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, (OrtMemoryInfo **)&meminfo);

    printf("READY %d %d\n", threads, wins);
    fflush(stdout);

    long total = 0;    // absolute samples pushed
    long decoded = 0;  // absolute samples covered by emitted windows
    int first = 1;
    int16_t eb[8192];
    for (;;) {
        int n = read(0, eb, sizeof(eb));
        if (n <= 0) break;
        n &= ~1;
        buf_push(eb, n / 2);
        total += n / 2;
        while (total - decoded >= wins) {
            int base = (int)(decoded - bufbase);
            decode_window(base, wins, stride_frames, first, 0, stdout);
            first = 0;
            decoded += stride;
            // compact: keep a quarter second before the next window
            int keep = (int)(decoded - RATE / 4 - bufbase);
            if (keep > 0) {
                memmove(buf, buf + keep, (bufn - keep) * sizeof(int16_t));
                bufn -= keep;
                bufbase += keep;
            }
        }
    }
    if (total - decoded > HOP_LEN * 4) {
        int base = (int)(decoded - bufbase);
        int len = bufn - base;
        if (len > 0) decode_window(base, len, 0, first, 1, stdout);
    }
    return 0;
}