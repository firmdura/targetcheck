/* SPDX-License-Identifier: Apache-2.0
 * Original synthetic process-lifecycle fixture; not Wget/ARM acceptance evidence.
 */
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

static const char *marker;
static void term(int sig) {
    (void)sig;
    int fd = open(marker, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd >= 0) { (void)write(fd, "term", 4); close(fd); }
    _exit(0);
}
static void ready(const char *file, pid_t child) {
    FILE *f = fopen(file, "w");
    if (!f) exit(80);
    fprintf(f, "%ld %ld\n", (long)getpid(), (long)child);
    fclose(f);
}
static void *stream(void *arg) {
    int fd = (int)(long)arg;
    char buf[4096]; memset(buf, fd == 1 ? 'O' : 'E', sizeof(buf));
    for (int i = 0; i < 16; i++) {
        if (write(fd, buf, sizeof(buf)) < 0) exit(81);
    }
    return NULL;
}
int main(int argc, char **argv) {
    const char *mode = getenv("TARGETCHECK_HELPER_MODE");
    if (!mode) return 82;
    if (!strcmp(mode, "args")) {
        for (int i = 0; i < argc; i++) printf("%zu:%s\n", strlen(argv[i]), argv[i]);
        char *cwd = getcwd(NULL, 0); if (!cwd) return 83;
        printf("cwd:%s\nenv:%s\n", cwd, getenv("TARGETCHECK_TEST_VALUE") ?: "unset");
        free(cwd); return 0;
    }
    if (!strcmp(mode, "exit")) return atoi(argv[1]);
    if (!strcmp(mode, "signal")) { raise(SIGTERM); return 84; }
    if (!strcmp(mode, "mark")) { ready(argv[1], 0); return 0; }
    if (!strcmp(mode, "sleep") || !strcmp(mode, "grace")) {
        if (!strcmp(mode, "grace")) { marker = argv[2]; signal(SIGTERM, term); }
        else signal(SIGTERM, SIG_IGN);
        ready(argv[1], 0);
        for (;;) pause();
    }
    if (!strcmp(mode, "child") || !strcmp(mode, "parent-exit")) {
        int syncpipe[2]; if (pipe(syncpipe)) return 85;
        pid_t child = fork(); if (child < 0) return 86;
        if (!child) {
            close(syncpipe[0]); signal(SIGTERM, SIG_IGN);
            (void)write(syncpipe[1], "R", 1); close(syncpipe[1]);
            for (;;) pause();
        }
        close(syncpipe[1]); char byte; if (read(syncpipe[0], &byte, 1) != 1) return 87;
        close(syncpipe[0]); signal(SIGTERM, SIG_IGN); ready(argv[1], child);
        if (!strcmp(mode, "parent-exit")) return 0;
        for (;;) pause();
    }
    if (!strcmp(mode, "both")) {
        pthread_t thread; if (pthread_create(&thread, NULL, stream, (void *)2L)) return 88;
        stream((void *)1L); pthread_join(thread, NULL); return 0;
    }
    if (!strcmp(mode, "exact")) {
        char buf[4096]; memset(buf, 'B', sizeof(buf));
        for (int i = 0; i < 256; i++) if (write(1, buf, sizeof(buf)) < 0) return 89;
        return 0;
    }
    if (!strcmp(mode, "flood-out") || !strcmp(mode, "flood-err")) {
        signal(SIGTERM, SIG_IGN);
        int fd = !strcmp(mode, "flood-out") ? 1 : 2;
        char buf[4096]; memset(buf, 'F', sizeof(buf));
        for (;;) if (write(fd, buf, sizeof(buf)) < 0) return 90;
    }
    return 91;
}
