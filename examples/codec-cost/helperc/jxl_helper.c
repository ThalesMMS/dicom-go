#include "jxl/decode.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_PAYLOAD (64u * 1024u * 1024u)

static int read_full(void *buf, size_t n) {
  unsigned char *p = buf;
  size_t got = 0;
  while (got < n) {
    size_t nread = fread(p + got, 1, n - got, stdin);
    if (nread == 0) {
      return 0;
    }
    got += nread;
  }
  return 1;
}

static int write_full(const void *buf, size_t n) {
  return fwrite(buf, 1, n, stdout) == n;
}

static int decode_jxl(const uint8_t *data, size_t size, uint32_t rows, uint32_t cols, uint32_t samples, uint32_t bits, uint8_t **out, uint32_t *out_len) {
  if (rows == 0 || cols == 0 || samples == 0) {
    return 1;
  }
  uint64_t bpp = bits > 8 ? 2u : 1u;
  if ((uint64_t)rows > UINT64_MAX / (uint64_t)cols) {
    return 1;
  }
  uint64_t want64 = (uint64_t)rows * (uint64_t)cols;
  if (want64 > UINT64_MAX / (uint64_t)samples) {
    return 1;
  }
  want64 *= (uint64_t)samples;
  if (want64 > UINT64_MAX / bpp) {
    return 1;
  }
  want64 *= bpp;
  if (want64 == 0 || want64 > MAX_PAYLOAD) {
    return 1;
  }
  JxlDecoder *dec = JxlDecoderCreate(NULL);
  if (dec == NULL) {
    return 1;
  }
  if (JxlDecoderSubscribeEvents(dec, JXL_DEC_BASIC_INFO | JXL_DEC_FULL_IMAGE) != JXL_DEC_SUCCESS) {
    JxlDecoderDestroy(dec);
    return 1;
  }
  if (JxlDecoderSetInput(dec, data, size) != JXL_DEC_SUCCESS) {
    JxlDecoderDestroy(dec);
    return 1;
  }
  JxlDecoderCloseInput(dec);
  JxlBasicInfo info;
  memset(&info, 0, sizeof(info));
  JxlPixelFormat format;
  memset(&format, 0, sizeof(format));
  format.num_channels = samples;
  format.data_type = bits > 8 ? JXL_TYPE_UINT16 : JXL_TYPE_UINT8;
  format.endianness = JXL_LITTLE_ENDIAN;
  format.align = 0;
  uint8_t *pixels = NULL;
  uint32_t want = (uint32_t)want64;
  for (;;) {
    JxlDecoderStatus st = JxlDecoderProcessInput(dec);
    if (st == JXL_DEC_ERROR) {
      free(pixels);
      JxlDecoderDestroy(dec);
      return 1;
    }
    if (st == JXL_DEC_NEED_MORE_INPUT) {
      free(pixels);
      JxlDecoderDestroy(dec);
      return 1;
    }
    if (st == JXL_DEC_BASIC_INFO) {
      if (JxlDecoderGetBasicInfo(dec, &info) != JXL_DEC_SUCCESS) {
        JxlDecoderDestroy(dec);
        return 1;
      }
      if (info.xsize != cols || info.ysize != rows) {
        JxlDecoderDestroy(dec);
        return 1;
      }
    }
    if (st == JXL_DEC_NEED_IMAGE_OUT_BUFFER) {
      size_t buffer_size = 0;
      if (JxlDecoderImageOutBufferSize(dec, &format, &buffer_size) != JXL_DEC_SUCCESS) {
        JxlDecoderDestroy(dec);
        return 1;
      }
      if (buffer_size > MAX_PAYLOAD) {
        JxlDecoderDestroy(dec);
        return 1;
      }
      pixels = malloc(buffer_size);
      if (pixels == NULL) {
        JxlDecoderDestroy(dec);
        return 1;
      }
      if (JxlDecoderSetImageOutBuffer(dec, &format, pixels, buffer_size) != JXL_DEC_SUCCESS) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return 1;
      }
      want = (uint32_t)buffer_size;
    }
    if (st == JXL_DEC_FULL_IMAGE || st == JXL_DEC_SUCCESS) {
      break;
    }
  }
  JxlDecoderDestroy(dec);
  if (pixels == NULL) {
    return 1;
  }
  *out = pixels;
  *out_len = want;
  return 0;
}

int main(void) {
  setvbuf(stdin, NULL, _IONBF, 0);
  setvbuf(stdout, NULL, _IONBF, 0);
  for (;;) {
    unsigned char header[24];
    if (!read_full(header, sizeof(header))) {
      return 0;
    }
    if (memcmp(header, "CST1", 4) != 0) {
      return 2;
    }
    uint32_t rows = (uint32_t)header[4] | ((uint32_t)header[5] << 8) | ((uint32_t)header[6] << 16) | ((uint32_t)header[7] << 24);
    uint32_t cols = (uint32_t)header[8] | ((uint32_t)header[9] << 8) | ((uint32_t)header[10] << 16) | ((uint32_t)header[11] << 24);
    uint32_t samples = (uint32_t)header[12] | ((uint32_t)header[13] << 8) | ((uint32_t)header[14] << 16) | ((uint32_t)header[15] << 24);
    uint32_t bits = (uint32_t)header[16] | ((uint32_t)header[17] << 8) | ((uint32_t)header[18] << 16) | ((uint32_t)header[19] << 24);
    uint32_t n = (uint32_t)header[20] | ((uint32_t)header[21] << 8) | ((uint32_t)header[22] << 16) | ((uint32_t)header[23] << 24);
    if (n == 0) {
      return 0;
    }
    if (n > MAX_PAYLOAD || rows == 0 || cols == 0 || samples == 0) {
      return 1;
    }
    uint8_t *payload = malloc(n);
    if (payload == NULL) {
      return 1;
    }
    if (!read_full(payload, n)) {
      free(payload);
      return 1;
    }
    uint8_t *pixels = NULL;
    uint32_t out_len = 0;
    int status = decode_jxl(payload, n, rows, cols, samples, bits, &pixels, &out_len);
    free(payload);
    unsigned char reply[12];
    memcpy(reply, "CST1", 4);
    reply[4] = (unsigned char)status;
    reply[5] = 0;
    reply[6] = 0;
    reply[7] = 0;
    uint32_t len = status == 0 ? out_len : 0;
    reply[8] = (unsigned char)len;
    reply[9] = (unsigned char)(len >> 8);
    reply[10] = (unsigned char)(len >> 16);
    reply[11] = (unsigned char)(len >> 24);
    if (!write_full(reply, sizeof(reply))) {
      free(pixels);
      return 1;
    }
    if (len > 0 && pixels != NULL && !write_full(pixels, len)) {
      free(pixels);
      return 1;
    }
    free(pixels);
  }
}
