#define _POSIX_C_SOURCE 200809L

#include "WhiteTransportTun2Socks.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

static void note_engine_failure(const char *operation) {
    char *detail = WTLastError();
    fprintf(stderr, "engine=%s result=failure\n", operation);
    if (detail != NULL) WTFreeCString(detail);
}

static int parse_rounds(void) {
    const char *value = getenv("WT_TEST_ROUNDS");
    long rounds = value == NULL ? 3 : strtol(value, NULL, 10);
    if (rounds < 1 || rounds > 3) {
        fprintf(stderr, "rounds result=invalid\n");
        return 0;
    }
    return (int)rounds;
}

static void write_round_marker(int round) {
    const char *path = getenv("WT_C_ABI_ROUND_FILE");
    if (path == NULL || path[0] == '\0') return;
    FILE *marker = fopen(path, "w");
    if (marker == NULL) return;
    fprintf(marker, "%d\n", round);
    fclose(marker);
}

static void redirect_child_log(void) {
    const char *path = getenv("WT_CLIENT_LOG");
    if (path == NULL || path[0] == '\0') return;
    int descriptor = open(path, O_WRONLY | O_CREAT | O_APPEND, 0600);
    if (descriptor < 0) _exit(126);
    if (dup2(descriptor, STDOUT_FILENO) < 0 || dup2(descriptor, STDERR_FILENO) < 0) _exit(126);
    close(descriptor);
}

static int child_exit_code(int status) {
    if (WIFEXITED(status)) return WEXITSTATUS(status);
    if (WIFSIGNALED(status)) return 128 + WTERMSIG(status);
    return 125;
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: runner <client> <socks-port>\n");
        return 64;
    }
    alarm(150);
    int rounds = parse_rounds();
    if (rounds == 0) return 64;
    fprintf(stderr, "proof_boundary=C-ABI-engine-smoke rounds=%d result=begin\n", rounds);

    int overall_failure = 0;
    int completed_rounds = 0;
    for (int round = 1; round <= rounds; round++) {
        write_round_marker(round);
        fprintf(stderr, "round=%d result=begin\n", round);
        int descriptors[2];
        if (socketpair(AF_UNIX, SOCK_DGRAM, 0, descriptors) != 0) {
            perror("socketpair");
            overall_failure = 1;
            break;
        }
        pid_t child = fork();
        if (child < 0) {
            perror("fork");
            close(descriptors[0]);
            close(descriptors[1]);
            overall_failure = 1;
            break;
        }
        if (child == 0) {
            close(descriptors[0]);
            char descriptor[32];
            char round_value[32];
            snprintf(descriptor, sizeof(descriptor), "%d", descriptors[1]);
            snprintf(round_value, sizeof(round_value), "%d", round);
            setenv("WT_TEST_ROUND", round_value, 1);
            redirect_child_log();
            execl(argv[1], argv[1], descriptor, NULL);
            _exit(127);
        }
        close(descriptors[1]);

        int start_status = WTStartTun2Socks((int32_t)descriptors[0], 1500, (int32_t)atoi(argv[2]));
        if (start_status != 0) note_engine_failure("start");

        int status = 0;
        int wait_status = waitpid(child, &status, 0);
        int client_exit = wait_status < 0 ? 125 : child_exit_code(status);
        int child_failure = wait_status < 0 || client_exit != 0;
        if (child_failure) {
            fprintf(stderr, "round=%d child_pid=%ld client_exit=%d child_failure=1\n", round, (long)child, client_exit);
        } else {
            fprintf(stderr, "round=%d child_pid=%ld client_exit=0 child_failure=0\n", round, (long)child);
        }

        int stop_status = WTStopTun2Socks();
        if (stop_status != 0) note_engine_failure("stop");

        errno = 0;
        int descriptor_closed = fcntl(descriptors[0], F_GETFD) == -1 && errno == EBADF;
        fprintf(stderr, "round=%d descriptor=ebadf result=%s\n", round, descriptor_closed ? "success" : "failure");
        if (!descriptor_closed) close(descriptors[0]);

        int round_failure = 0;
        if (start_status != 0) round_failure = start_status;
        if (round_failure == 0 && child_failure) round_failure = client_exit == 0 ? 1 : client_exit;
        if (round_failure == 0 && stop_status != 0) round_failure = stop_status;
        if (round_failure == 0 && !descriptor_closed) round_failure = 1;
        if (round_failure != 0) {
            overall_failure = round_failure;
            fprintf(stderr, "round=%d result=failure\n", round);
            break;
        }
        completed_rounds = round;
        fprintf(stderr, "round=%d result=success\n", round);
    }

    if (overall_failure != 0) {
        fprintf(stderr, "interpretation=fail proof_boundary=C-ABI-engine-smoke rounds=%d result=failure\n", completed_rounds + 1);
        return overall_failure;
    }
    printf("interpretation=pass proof_boundary=C-ABI-engine-smoke rounds=%d result=success\n", completed_rounds);
    return 0;
}
