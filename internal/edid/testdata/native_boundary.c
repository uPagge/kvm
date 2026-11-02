#include <stdarg.h>
#include <assert.h>

// System declarations are renamed too, preserving their actual signatures.
#define open test_open
#define close test_close
#define ioctl test_ioctl
#define klogctl test_klogctl
#include "edid.c"
#include "hex_parser.inc"

static int open_calls, requested_blocks, written_blocks, returned_blocks = 1;
int test_open(const char *path, int flags, ...) {
    (void)path; (void)flags; open_calls++; return 100;
}
int test_close(int fd) { (void)fd; return 0; }
int test_ioctl(int fd, unsigned long request, ...) {
    (void)fd;
    va_list args; va_start(args, request);
    struct v4l2_edid *value = va_arg(args, struct v4l2_edid *);
    va_end(args);
    if (request == VIDIOC_G_EDID) {
        requested_blocks = value->blocks;
        value->blocks = returned_blocks;
    }
    if (request == VIDIOC_S_EDID) { written_blocks = value->blocks; }
    return 0;
}
int test_klogctl(int type, char *buf, int len) {
    (void)type; (void)buf; (void)len; return -1;
}
void log_message(int level, const char *file, const char *func, int line, const char *message) {
    (void)level; (void)file; (void)func; (void)line; (void)message;
}

int main(int argc, char **argv) {
    assert(argc == 2);
    uint8_t data[256] = {0};
    if (strcmp(argv[1], "checksum") == 0) {
        const uint8_t header[8] = {0,255,255,255,255,255,255,0};
        memcpy(data, header, 8);
        uint8_t before[128]; memcpy(before, data, 128);
        assert(set_edid(data, 128) == -1);
        assert(errno == EINVAL);
        assert(open_calls == 0);
        assert(memcmp(data, before, 128) == 0);
    } else if (strcmp(argv[1], "valid_set") == 0) {
        memset(data + 1, 255, 6); data[127] = 6;
        uint8_t before[128]; memcpy(before, data, 128);
        assert(set_edid(data, 128) == 0);
        assert(open_calls == 1 && written_blocks == 1);
        assert(memcmp(data, before, 128) == 0);
    } else if (strcmp(argv[1], "disabled_set") == 0) {
        assert(set_edid(data, 0) == 0);
        assert(open_calls == 1 && written_blocks == 0);
    } else if (strcmp(argv[1], "read_capacity") == 0) {
        assert(get_edid(data, 128) == 128);
        assert(requested_blocks == 1);
    } else if (strcmp(argv[1], "empty_read") == 0) {
        returned_blocks = 0;
        assert(get_edid(data, 256) == -1);
    } else if (strcmp(argv[1], "oversize_read") == 0) {
        returned_blocks = 3;
        assert(get_edid(data, 256) == -1);
    } else if (strcmp(argv[1], "invalid_hex") == 0) {
        assert(hex_to_bytes("+1", data, 256) == -1);
        assert(hex_to_bytes(" 1", data, 256) == -1);
        assert(hex_to_bytes("FF", data, 256) == 1 && data[0] == 255);
        assert(hex_to_bytes("", data, 256) == 0);
    } else { return 2; }
    return 0;
}
