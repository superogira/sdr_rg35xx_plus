// deepcw_bench — prototype/benchmark for the DeepCW neural CW decoder
// (model + metadata from github.com/e04/deepcw-engine, AGPL-3.0-only).
//
// Reads a mono PCM WAV (any rate), resamples to the model rate, builds
// the spectrogram exactly as the reference Python example does, runs the
// ONNX model on the CPU and prints: spectrogram time, inference time,
// real-time factor, peak RSS and the greedy CTC decode.
//
// Usage: deepcw_bench model.onnx model.onnx.json input.wav [threads] [window_s]
//   window_s: split the audio into overlapping windows of that many
//   seconds (stride = window/4) to mimic streaming; 0 = one big call.
//
// This file is a measurement harness, not shipped code.
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <time.h>

#include "onnxruntime_c_api.h"

// Metadata constants (third_party/deepcw/model.onnx.json).
#define SAMPLE_RATE 3200
#define FFT_LEN 256
#define HOP_LEN 48
#define BIN_LO 32 // ceil(400/12.5)
#define BIN_HI 97 // floor(1200/12.5)+1
#define NBINS (BIN_HI - BIN_LO) // 65
#define NCLASS 42
#define BLANK 41

static double now_s(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec + ts.tv_nsec * 1e-9;
}

// ---- minimal WAV reader (s16 mono or stereo->mono) ----
static int16_t *read_wav(const char *path, int *rate, int *nsamples) {
    FILE *f = fopen(path, "rb");
    if (!f) return NULL;
    uint8_t hdr[36];
    if (fread(hdr, 1, 36, f) != 36) { fclose(f); return NULL; }
    if (memcmp(hdr, "RIFF", 4) || memcmp(hdr + 8, "WAVE", 4)) { fclose(f); return NULL; }
    int ch = hdr[22] | hdr[23] << 8;
    int bw = hdr[34] | hdr[35] << 8;
    *rate = hdr[24] | hdr[25] << 8 | hdr[26] << 16 | hdr[27] << 24;
    if (bw != 16) { fclose(f); fprintf(stderr, "only s16 wav\n"); return NULL; }
    // find data chunk
    uint32_t dlen = 0;
    for (;;) {
        uint8_t chdr[8];
        if (fread(chdr, 1, 8, f) != 8) { fclose(f); return NULL; }
        dlen = chdr[4] | chdr[5] << 8 | chdr[6] << 16 | chdr[7] << 24;
        if (!memcmp(chdr, "data", 4)) break;
        fseek(f, dlen, SEEK_CUR);
    }
    int16_t *raw = malloc(dlen);
    if (fread(raw, 1, dlen, f) != dlen) { free(raw); fclose(f); return NULL; }
    fclose(f);
    int n = dlen / 2 / ch;
    int16_t *mono = malloc(n * 2);
    for (int i = 0; i < n; i++) {
        int acc = 0;
        for (int c = 0; c < ch; c++) acc += raw[i * ch + c];
        mono[i] = (int16_t)(acc / ch);
    }
    free(raw);
    *nsamples = n;
    return mono;
}

static float *resample_linear(const int16_t *in, int n, int src, int dst, int *outn) {
    int m = (int)llround((double)n * dst / src);
    float *out = malloc(m * sizeof(float));
    for (int i = 0; i < m; i++) {
        double p = (double)i * src / dst;
        int l = (int)p;
        if (l > n - 1) l = n - 1;
        int r = l + 1 < n ? l + 1 : n - 1;
        double f = p - l;
        out[i] = (float)((in[l] * (1 - f) + in[r] * f) / 32768.0);
    }
    *outn = m;
    return out;
}

// ---- radix-2 FFT (in place, complex) ----
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

// spectrogram exactly like the reference: reflect-pad fft/2, hann(256),
// hop 48, |rfft| bins 32..96, log1p.
static float *spectrogram(const float *audio, int n, int *frames) {
    int pad = FFT_LEN / 2;
    int pn = n + 2 * pad;
    float *p = malloc(pn * sizeof(float));
    for (int i = 0; i < pad; i++) p[i] = audio[pad - i < n ? pad - i : 0];
    memcpy(p + pad, audio, n * sizeof(float));
    for (int i = 0; i < pad; i++) p[pad + n + i] = audio[n - 1 - i > 0 ? n - 1 - i : 0];
    static float win[FFT_LEN];
    static int wininit = 0;
    if (!wininit) {
        for (int i = 0; i < FFT_LEN; i++) win[i] = (float)(0.5 - 0.5 * cos(2 * M_PI * i / FFT_LEN));
        wininit = 1;
    }
    int fr = 1 + (pn - FFT_LEN) / HOP_LEN;
    float *spec = malloc((size_t)fr * NBINS * sizeof(float));
    float re[FFT_LEN], im[FFT_LEN];
    for (int f = 0; f < fr; f++) {
        for (int k = 0; k < FFT_LEN; k++) {
            re[k] = p[f * HOP_LEN + k] * win[k];
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

static const char CHARS[] = ",./0123456789?ABCDEFGHIJKLMNOPQRSTUVWXYZ ";

int main(int argc, char **argv) {
    if (argc < 4) {
        fprintf(stderr, "usage: %s model.onnx model.onnx.json in.wav [threads] [window_s]\n", argv[0]);
        return 2;
    }
    int threads = argc > 4 ? atoi(argv[4]) : 1;
    double window = argc > 5 ? atof(argv[5]) : 0;

    int rate = 0, n = 0;
    int16_t *raw = read_wav(argv[3], &rate, &n);
    if (!raw) { fprintf(stderr, "wav read fail\n"); return 2; }
    int an = 0;
    float *audio = resample_linear(raw, n, rate, SAMPLE_RATE, &an);
    free(raw);
    double secs = (double)an / SAMPLE_RATE;
    printf("audio: %d src samples @%dHz -> %d @3200Hz (%.2fs)\n", n, rate, an, secs);

    const OrtApi *ort = OrtGetApiBase()->GetApi(ORT_API_VERSION);
    OrtEnv *env = NULL;
    ort->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "deepcw", &env);
    OrtSessionOptions *opts = NULL;
    ort->CreateSessionOptions(&opts);
    ort->SetIntraOpNumThreads(opts, threads);
    ort->SetSessionGraphOptimizationLevel(opts, ORT_ENABLE_ALL);
    OrtSession *sess = NULL;
    double t0 = now_s();
    OrtStatus *st = ort->CreateSession(env, argv[1], opts, &sess);
    if (st) { fprintf(stderr, "session: %s\n", ort->GetErrorMessage(st)); return 2; }
    printf("session load: %.2fs (threads=%d)\n", now_s() - t0, threads);

    OrtAllocator *alloc = NULL;
    ort->GetAllocatorWithDefaultOptions(&alloc);
    char *inname = NULL, *outname = NULL;
    ort->SessionGetInputName(sess, 0, alloc, &inname);
    ort->SessionGetOutputName(sess, 0, alloc, &outname);
    const OrtMemoryInfo *mi = NULL;
    ort->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, (OrtMemoryInfo **)&mi);

    // Windowing: stride = window/4 (75% overlap) to mimic streaming;
    // window=0 = single call over the whole clip.
    int win_samples = window > 0 ? (int)(window * SAMPLE_RATE) : an;
    // Keep a trailing margin of future audio in every window so the
    // model sees context past the emit boundary; the next window
    // starts where this one stopped emitting.
    int margin = window > 0 ? SAMPLE_RATE / 2 : 0;
    int stride = window > 0 ? win_samples - margin : an;
    if (win_samples > an) win_samples = an;
    int margin_frames = margin / HOP_LEN;

    double spec_total = 0, infer_total = 0;
    long calls = 0;
    char text[4096];
    size_t tlen = 0;
    text[0] = 0;

    for (int off = 0; off < an; off += stride) {
        int len = an - off;
        if (len > win_samples) len = win_samples;
        if (len < FFT_LEN) break;
        int fr = 0;
        double ts = now_s();
        float *spec = spectrogram(audio + off, len, &fr);
        spec_total += now_s() - ts;

        int64_t dims[4] = {1, 1, fr, NBINS};
        OrtValue *in = NULL;
        st = ort->CreateTensorWithDataAsOrtValue(mi, spec, (size_t)fr * NBINS * 4, dims, 4,
                                                 ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT, &in);
        if (st) { fprintf(stderr, "tensor: %s\n", ort->GetErrorMessage(st)); return 2; }
        OrtValue *outv = NULL;
        const char *ins[1] = {inname};
        const char *outs[1] = {outname};
        ts = now_s();
        st = ort->Run(sess, NULL, ins, (const OrtValue *const *)&in, 1, outs, 1, &outv);
        infer_total += now_s() - ts;
        if (st) { fprintf(stderr, "run: %s\n", ort->GetErrorMessage(st)); return 2; }
        float *lp = NULL;
        ort->GetTensorMutableData(outv, (void **)&lp);
        int64_t odims[3];
        OrtTensorTypeAndShapeInfo *ti = NULL;
        ort->GetTensorTypeAndShape(outv, &ti);
        ort->GetDimensions(ti, odims, 3);
        ort->ReleaseTensorTypeAndShapeInfo(ti);
        int T = (int)odims[1];
        // Streaming dedupe: with 75% overlap only the LAST quarter of
        // each window carries new symbols; emitting whole windows
        // duplicates characters at the seams.
        int tend = (window > 0 && calls > 0) ? T - margin_frames : T;
        if (tend > T) tend = T;
        if (tend < 0) tend = 0;
        // greedy CTC over the stable interior only
        int prev = -1;
        for (int t = 0; t < tend; t++) {
            int best = 0;
            float bv = lp[(size_t)t * NCLASS];
            for (int c = 1; c < NCLASS; c++) {
                if (lp[(size_t)t * NCLASS + c] > bv) { bv = lp[(size_t)t * NCLASS + c]; best = c; }
            }
            if (best == BLANK) { prev = -1; continue; }
            if (best != prev && tlen + 1 < sizeof(text)) text[tlen++] = CHARS[best];
            prev = best;
        }
        text[tlen] = 0;
        free(spec);
        ort->ReleaseValue(in);
        ort->ReleaseValue(outv);
        calls++;
        if (window <= 0) break;
        if (off + win_samples >= an) break;
    }

    struct rusage ru;
    getrusage(RUSAGE_SELF, &ru);
    printf("calls=%ld spectrogram=%.3fs inference=%.3fs (total %.3fs for %.2fs audio)\n",
           calls, spec_total, infer_total, spec_total + infer_total, secs);
    printf("real-time factor: %.2fx (inference alone %.2fx)\n",
           secs / (spec_total + infer_total), secs / infer_total);
    double cpu = (ru.ru_utime.tv_sec + ru.ru_utime.tv_usec * 1e-6) +
                 (ru.ru_stime.tv_sec + ru.ru_stime.tv_usec * 1e-6);
    double wall = spec_total + infer_total + 0.6;
    printf("peak RSS: %.1f MB, CPU time %.2fs = %.0f%% of one core\n",
           ru.ru_maxrss / 1024.0, cpu, 100 * cpu / wall);
    printf("decode: %s\n", text);
    return 0;
}