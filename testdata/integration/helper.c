/* SPDX-License-Identifier: Apache-2.0 */
/* Synthetic process fixture. This is not the real Wget acceptance example. */
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <signal.h>
#include <stdlib.h>
int main(int argc, char **argv) {
 char mode[64] = {0};
 FILE *f = fopen("mode.txt", "r");
 if (!f || !fgets(mode, sizeof mode, f)) return 98;
 fclose(f);
 mode[strcspn(mode, "\r\n")] = 0;
 if (argc > 1 && strcmp(argv[1], "args") == 0) {
  printf("%s|%s|%s\n", argc > 2 ? argv[2] : "MISSING", argc > 3 ? argv[3] : "MISSING", argc > 4 ? argv[4] : "MISSING");
  return 0;
 }
 if (!strcmp(mode, "negative")) { puts("BASELINE_DEFECT"); return 7; }
 if (!strcmp(mode, "fail")) { puts("CANDIDATE_FAIL"); return 3; }
 if (!strcmp(mode, "sleep")) {
  f = fopen("started", "w"); if (!f) return 97; fputs("started", f); fclose(f);
  for (;;) pause();
 }
 if (!strcmp(mode, "flood")) { for (int i=0; i<2097152; ++i) putchar('x'); fflush(stdout); return 0; }
 if (!strcmp(mode, "signal")) { raise(SIGTERM); return 99; }
 if (!strcmp(mode, "mutate")) { if (rename("replacement", "runner")) return 96; puts("CASE_OK"); return 0; }
 puts("CASE_OK");
 return 0;
}
