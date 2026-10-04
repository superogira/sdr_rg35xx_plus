// gpsread — user-space NMEA reader for CDC-ACM GPS dongles (u-blox etc).
//
// The RG35XX firmware kernel ships no cdc_acm/usbserial modules, so a
// /dev/ttyACM device never appears. This helper talks to the dongle
// through libusb instead: it scans for any device exposing a CDC Data
// interface (class 10) with a bulk IN endpoint, claims it and streams
// the NMEA bytes to stdout. Receivers send NMEA at their power-up
// default (u-blox: 9600 8N1) without needing the control interface.
//
// Build:  gcc -O2 -o gpsread gpsread.c -lusb-1.0
// Use:    ./gpsread   (the app spawns it next to its own binary)
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <libusb-1.0/libusb.h>

static libusb_device_handle *open_gps(libusb_context *ctx,
                                      uint8_t *ifnum, uint8_t *ep_in) {
    libusb_device **devs;
    ssize_t n = libusb_get_device_list(ctx, &devs);
    if (n < 0) return NULL;
    libusb_device_handle *found = NULL;
    for (ssize_t i = 0; i < n && !found; i++) {
        struct libusb_config_descriptor *cfg;
        if (libusb_get_active_config_descriptor(devs[i], &cfg) < 0)
            continue;
        for (int j = 0; j < cfg->bNumInterfaces && !found; j++) {
            const struct libusb_interface *itf = &cfg->interface[j];
            const struct libusb_interface_descriptor *id =
                &itf->altsetting[0];
            if (id->bInterfaceClass != 10) // LIBUSB_CLASS_CDC_DATA
                continue;
            for (int e = 0; e < id->bNumEndpoints; e++) {
                const struct libusb_endpoint_descriptor *ep =
                    &id->endpoint[e];
                if ((ep->bEndpointAddress & 0x80) &&
                    (ep->bmAttributes & 3) == 2) { // bulk IN
                    libusb_device_handle *h;
                    if (libusb_open(devs[i], &h) == 0) {
                        *ifnum = id->bInterfaceNumber;
                        *ep_in = ep->bEndpointAddress;
                        found = h;
                    }
                    break;
                }
            }
        }
        libusb_free_config_descriptor(cfg);
    }
    libusb_free_device_list(devs, 1);
    return found;
}

int main(void) {
    libusb_context *ctx;
    if (libusb_init(&ctx) < 0) {
        fprintf(stderr, "gpsread: libusb init failed\n");
        return 1;
    }
    uint8_t ifnum = 0, ep_in = 0;
    libusb_device_handle *h = open_gps(ctx, &ifnum, &ep_in);
    if (!h) {
        fprintf(stderr, "gpsread: no CDC-ACM GPS device found\n");
        return 1;
    }
    int r = libusb_claim_interface(h, ifnum);
    if (r != 0 && libusb_kernel_driver_active(h, ifnum) == 1) {
        libusb_detach_kernel_driver(h, ifnum);
        r = libusb_claim_interface(h, ifnum);
    }
    if (r != 0) {
        fprintf(stderr, "gpsread: claim interface %d: %s\n", ifnum,
                libusb_error_name(r));
        return 1;
    }
    setvbuf(stdout, NULL, _IOLBF, 0);
    fprintf(stderr, "gpsread: streaming interface %d ep 0x%02x\n",
            ifnum, ep_in);
    unsigned char buf[512];
    for (;;) {
        int got = 0;
        r = libusb_bulk_transfer(h, ep_in, buf, sizeof buf, &got, 1000);
        if (r == 0 || r == LIBUSB_ERROR_TIMEOUT) {
            if (got > 0 && fwrite(buf, 1, got, stdout) == (size_t)got)
                fflush(stdout);
            else if (got > 0)
                break;
        } else {
            fprintf(stderr, "gpsread: bulk: %s\n", libusb_error_name(r));
            break; // device unplugged — exit; the app rescans
        }
    }
    libusb_close(h);
    libusb_exit(ctx);
    return 0;
}
