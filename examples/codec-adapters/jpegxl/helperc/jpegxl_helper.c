#include "jxl/decode.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#endif

#define HELPER_MAGIC "JXLH"
#define HELPER_VERSION 1u
#define OPCODE_HELLO 1u
#define OPCODE_DECODE 2u
#define OPCODE_SHUTDOWN 3u
#define STATUS_OK 0u
#define STATUS_MALFORMED 1u
#define STATUS_UNSUPPORTED 2u
#define STATUS_SIZE 3u
#define STATUS_TOO_LARGE 4u
#define STATUS_PROTOCOL 5u
#define MAX_PAYLOAD (64u * 1024u * 1024u)

static uint32_t read_u32(const unsigned char *p) {
  return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

static uint64_t read_u64(const unsigned char *p) {
  return (uint64_t)read_u32(p) | ((uint64_t)read_u32(p + 4) << 32);
}

static void write_u32(unsigned char *p, uint32_t v) {
  p[0] = (unsigned char)v;
  p[1] = (unsigned char)(v >> 8);
  p[2] = (unsigned char)(v >> 16);
  p[3] = (unsigned char)(v >> 24);
}

static void write_u64(unsigned char *p, uint64_t v) {
  write_u32(p, (uint32_t)v);
  write_u32(p + 4, (uint32_t)(v >> 32));
}

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

static int drain_input(uint64_t n) {
  unsigned char scratch[4096];
  while (n > 0) {
    size_t chunk = n < sizeof(scratch) ? (size_t)n : sizeof(scratch);
    if (!read_full(scratch, chunk)) {
      return 0;
    }
    n -= chunk;
  }
  return 1;
}

static int write_reply(uint64_t request_id, uint32_t status, uint32_t rows, uint32_t cols, uint32_t samples, const uint8_t *pixels, uint32_t n) {
  unsigned char header[36];
  memset(header, 0, sizeof(header));
  memcpy(header, HELPER_MAGIC, 4);
  write_u32(header + 4, HELPER_VERSION);
  write_u64(header + 8, request_id);
  write_u32(header + 16, status);
  write_u32(header + 20, rows);
  write_u32(header + 24, cols);
  write_u32(header + 28, samples);
  if (status != STATUS_OK || pixels == NULL) {
    n = 0;
    pixels = NULL;
  }
  write_u32(header + 32, n);
  if (!write_full(header, sizeof(header))) {
    return 0;
  }
  if (n > 0 && pixels != NULL && !write_full(pixels, n)) {
    return 0;
  }
  return 1;
}

static int decode_jxl(const uint8_t *data, size_t size, uint32_t rows, uint32_t cols, uint32_t samples, uint32_t bits, uint8_t **out, uint32_t *out_len) {
  if (rows == 0 || cols == 0 || (samples != 1 && samples != 3) || (bits != 8 && bits != 16)) {
    return STATUS_UNSUPPORTED;
  }
  uint64_t want64 = (uint64_t)rows * (uint64_t)cols * (uint64_t)samples * (bits > 8 ? 2u : 1u);
  if (want64 == 0 || want64 > MAX_PAYLOAD) {
    return STATUS_TOO_LARGE;
  }
  JxlDecoder *dec = JxlDecoderCreate(NULL);
  if (dec == NULL) {
    return STATUS_MALFORMED;
  }
  if (JxlDecoderSubscribeEvents(dec, JXL_DEC_BASIC_INFO | JXL_DEC_FULL_IMAGE) != JXL_DEC_SUCCESS) {
    JxlDecoderDestroy(dec);
    return STATUS_MALFORMED;
  }
  if (JxlDecoderSetInput(dec, data, size) != JXL_DEC_SUCCESS) {
    JxlDecoderDestroy(dec);
    return STATUS_MALFORMED;
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
    if (st == JXL_DEC_ERROR || st == JXL_DEC_NEED_MORE_INPUT) {
      free(pixels);
      JxlDecoderDestroy(dec);
      return STATUS_MALFORMED;
    }
    if (st == JXL_DEC_BASIC_INFO) {
      if (JxlDecoderGetBasicInfo(dec, &info) != JXL_DEC_SUCCESS) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return STATUS_MALFORMED;
      }
      if (info.xsize != cols || info.ysize != rows) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return STATUS_SIZE;
      }
    }
    if (st == JXL_DEC_NEED_IMAGE_OUT_BUFFER) {
      size_t buffer_size = 0;
      if (JxlDecoderImageOutBufferSize(dec, &format, &buffer_size) != JXL_DEC_SUCCESS) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return STATUS_MALFORMED;
      }
      if (buffer_size > MAX_PAYLOAD) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return STATUS_TOO_LARGE;
      }
      free(pixels);
      pixels = malloc(buffer_size);
      if (pixels == NULL) {
        JxlDecoderDestroy(dec);
        return STATUS_TOO_LARGE;
      }
      if (JxlDecoderSetImageOutBuffer(dec, &format, pixels, buffer_size) != JXL_DEC_SUCCESS) {
        free(pixels);
        JxlDecoderDestroy(dec);
        return STATUS_MALFORMED;
      }
      want = (uint32_t)buffer_size;
    }
    if (st == JXL_DEC_FULL_IMAGE || st == JXL_DEC_SUCCESS) {
      break;
    }
  }
  JxlDecoderDestroy(dec);
  if (pixels == NULL) {
    return STATUS_MALFORMED;
  }
  *out = pixels;
  *out_len = want;
  return STATUS_OK;
}

int main(void) {
#ifdef _WIN32
  _setmode(_fileno(stdin), _O_BINARY);
  _setmode(_fileno(stdout), _O_BINARY);
#endif
  setvbuf(stdin, NULL, _IONBF, 0);
  setvbuf(stdout, NULL, _IONBF, 0);
  for (;;) {
    unsigned char header[40];
    if (!read_full(header, sizeof(header))) {
      return 0;
    }
    if (memcmp(header, HELPER_MAGIC, 4) != 0 || read_u32(header + 4) != HELPER_VERSION) {
      return 2;
    }
    uint32_t opcode = read_u32(header + 8);
    uint64_t request_id = read_u64(header + 12);
    uint32_t rows = read_u32(header + 20);
    uint32_t cols = read_u32(header + 24);
    uint32_t samples = read_u32(header + 28);
    uint32_t bits = read_u32(header + 32);
    uint32_t n = read_u32(header + 36);
    if (opcode == OPCODE_SHUTDOWN) {
      return 0;
    }
    if (opcode == OPCODE_HELLO) {
      if (n > 0 && !drain_input(n)) {
        return 1;
      }
      if (!write_reply(request_id, STATUS_OK, 0, 0, 0, NULL, 0)) {
        return 1;
      }
      continue;
    }
    if (opcode != OPCODE_DECODE) {
      if (n > 0 && !drain_input(n)) {
        return 1;
      }
      if (!write_reply(request_id, STATUS_PROTOCOL, 0, 0, 0, NULL, 0)) {
        return 1;
      }
      continue;
    }
    if (n == 0) {
      if (!write_reply(request_id, STATUS_MALFORMED, rows, cols, samples, NULL, 0)) {
        return 1;
      }
      continue;
    }
    if (n > MAX_PAYLOAD) {
      if (!drain_input(n)) {
        return 1;
      }
      if (!write_reply(request_id, STATUS_TOO_LARGE, rows, cols, samples, NULL, 0)) {
        return 1;
      }
      continue;
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
    if (!write_reply(request_id, (uint32_t)status, rows, cols, samples, pixels, out_len)) {
      free(pixels);
      return 1;
    }
    free(pixels);
  }
}
