// feeder — write a file to stdout at a fixed byte rate (test harness).
// usage: feeder file bytes_per_chunk ms_per_chunk
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc < 4) return 2;
    FILE *f = fopen(argv[1], "rb");
    if (!f) return 2;
    int chunk = atoi(argv[2]);
    useconds_t ms = (useconds_t)(atoi(argv[3]) * 1000);
    unsigned char *b = malloc(chunk);
    for (;;) {
        int n = fread(b, 1, chunk, f);
        if (n <= 0) break;
        int off = 0;
        while (off < n) {
            int w = write(1, b + off, n - off);
            if (w <= 0) return 1;
            off += w;
        }
        usleep(ms);
    }
    return 0;
}