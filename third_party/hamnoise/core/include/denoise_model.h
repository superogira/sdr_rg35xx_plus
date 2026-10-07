#ifndef DENOISE_MODEL_H
#define DENOISE_MODEL_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DENOISE_SAMPLE_RATE 9600
#define DENOISE_FFT_LENGTH 256
#define DENOISE_HOP_LENGTH 144
#define DENOISE_SPECTROGRAM_START_BIN 0
#define DENOISE_SPECTROGRAM_STOP_BIN_EXCLUSIVE 129
#define DENOISE_INPUT_BINS 129
#define DENOISE_HIDDEN_SIZE 64
#define DENOISE_GRU_GATES 3
#define DENOISE_LAYER_NORM_EPS 1.0e-5f

#if DENOISE_SPECTROGRAM_STOP_BIN_EXCLUSIVE > ((DENOISE_FFT_LENGTH / 2) + 1)
#error "DENOISE_SPECTROGRAM_STOP_BIN_EXCLUSIVE exceeds the real FFT bin range"
#endif

#if DENOISE_INPUT_BINS != (DENOISE_SPECTROGRAM_STOP_BIN_EXCLUSIVE - DENOISE_SPECTROGRAM_START_BIN)
#error "DENOISE_INPUT_BINS must match the configured spectrogram band"
#endif

typedef struct {
    uint32_t input_bins;
    uint32_t hidden_size;
    float max_gain;
    float layer_norm_eps;

    /* Optional band-split front end (cw_denoise_gru_mask_bs). num_bands == 0
     * disables it and the normalized features feed the GRU directly. */
    uint32_t num_bands;
    uint32_t encoder_size;            /* sum of band_embed_dims; GRU input size when num_bands > 0 */
    const int *band_widths;           /* [num_bands] */
    const int *band_embed_dims;       /* [num_bands] */
    const int *band_input_offsets;    /* [num_bands + 1], cumulative widths ending at input_bins */
    const int *band_embed_offsets;    /* [num_bands + 1], cumulative embed dims ending at encoder_size */
    const float *band_encoder_weight; /* concatenated per-band [embed_dim, width] row-major blocks */
    const float *band_encoder_bias;   /* concatenated per-band [embed_dim] blocks */

    const float *input_norm_weight;   /* [input_bins] */
    const float *input_norm_bias;     /* [input_bins] */

    const float *gru_weight_ih;       /* [3 * hidden_size, gru_input], PyTorch gate order: reset, update, new */
    const float *gru_weight_hh;       /* [3 * hidden_size, hidden_size], PyTorch gate order: reset, update, new */
    const float *gru_bias_ih;         /* [3 * hidden_size] */
    const float *gru_bias_hh;         /* [3 * hidden_size] */

    const float *output_norm_weight;  /* [hidden_size] */
    const float *output_norm_bias;    /* [hidden_size] */
    const float *fc1_weight;          /* [hidden_size, hidden_size] */
    const float *fc1_bias;            /* [hidden_size] */
    const float *fc2_weight;          /* [input_bins, hidden_size] */
    const float *fc2_bias;            /* [input_bins] */
} denoise_model_t;

/* The band-split export writes the same struct; the band fields are simply populated. */
typedef denoise_model_t denoise_model_bs_t;

int denoise_model_validate(const denoise_model_t *model);

#ifdef __cplusplus
}
#endif

#endif
