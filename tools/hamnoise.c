/* hamnoise — SDRg35xx sidecar wrapping the HamNoise (AGPL-3.0) core.
 *
 * Protocol: raw float32 mono at 9600 Hz on stdin, denoised float32 at
 * 9600 Hz on stdout, hop-aligned (144 samples per produced block; the
 * first hops produce nothing while the overlap-add window fills).
 * argv[1] selects the model: "voice" (default) or "cw".
 *
 * Kept as a separate process on purpose: HamNoise is AGPL-3.0 and the
 * main app must stay free of it — the same pattern this project uses
 * for the GPL rtl_tcp sidecar.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "denoise_audio.h"
#include "denoise_model.h"
#include "cw_model_weights.h"
#include "voice_model_weights.h"

#define HOP DENOISE_HOP_LENGTH

int main(int argc, char **argv) {
    const denoise_model_t *model = &k_voice_reduction_model;
    if (argc > 1 && strcmp(argv[1], "cw") == 0) {
        model = &k_denoise_model;
    }
    if (denoise_model_validate(model) != 0) {
        fprintf(stderr, "hamnoise: model validate failed\n");
        return 1;
    }
    denoise_stream_t stream;
    if (denoise_stream_init(&stream) != 0) {
        fprintf(stderr, "hamnoise: stream init failed\n");
        return 1;
    }
    /* Unbuffered binary pipes; stdout is written in whole hops. */
    setvbuf(stdin, NULL, _IONBF, 0);
    setvbuf(stdout, NULL, _IONBF, 0);

    float in[HOP], out[HOP];
    for (;;) {
        size_t got = fread(in, sizeof(float), HOP, stdin);
        if (got == 0) {
            break; /* EOF or error: parent closed the pipe */
        }
        if (got < HOP) {
            memset(in + got, 0, (HOP - got) * sizeof(float));
        }
        bool produced = false;
        if (denoise_stream_process_hop(&stream, model, in, out, &produced) != 0) {
            fprintf(stderr, "hamnoise: process error\n");
            return 1;
        }
        /* Always emit one hop per input hop (zeros while the
         * overlap-add window fills) so the parent's pipe stays
         * exactly 1:1 and never has to guess the warm-up length. */
        if (!produced) {
            memset(out, 0, sizeof(out));
        }
        if (fwrite(out, sizeof(float), HOP, stdout) != HOP) {
            return 0; /* reader gone */
        }
    }
    return 0;
}
