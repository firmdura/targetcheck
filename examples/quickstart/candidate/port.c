/* SPDX-License-Identifier: Apache-2.0 */
/*
 * TargetCheck quickstart component, candidate variant. Synthetic example code.
 *
 * parse_port() converts a decimal TCP port number. This candidate fixes the
 * baseline defect: it rejects values above 65535.
 *
 * Usage: port-check CASE
 *   valid-port           "8080" must be accepted as 8080
 *   reject-out-of-range  "70000" must be rejected
 * Exit 0 and "PASS CASE" when the case holds, exit 1 and "FAIL CASE: ..."
 * when it does not, exit 2 for a usage error.
 */
#include <stdio.h>
#include <string.h>

static int parse_port(const char *text, unsigned short *port)
{
	unsigned long value = 0;

	if (*text == '\0')
		return -1;
	for (; *text != '\0'; text++) {
		if (*text < '0' || *text > '9')
			return -1;
		value = value * 10 + (unsigned long)(*text - '0');
		if (value > 65535)
			return -1;
	}
	*port = (unsigned short)value;
	return 0;
}

int main(int argc, char **argv)
{
	unsigned short port = 0;

	if (argc != 2) {
		fprintf(stderr, "usage: port-check CASE\n");
		return 2;
	}
	if (strcmp(argv[1], "valid-port") == 0) {
		if (parse_port("8080", &port) != 0 || port != 8080) {
			printf("FAIL valid-port: \"8080\" was not accepted as 8080\n");
			return 1;
		}
		printf("PASS valid-port\n");
		return 0;
	}
	if (strcmp(argv[1], "reject-out-of-range") == 0) {
		if (parse_port("70000", &port) == 0) {
			printf("FAIL reject-out-of-range: \"70000\" was accepted as %u\n", (unsigned)port);
			return 1;
		}
		printf("PASS reject-out-of-range\n");
		return 0;
	}
	fprintf(stderr, "port-check: unknown case \"%s\"\n", argv[1]);
	return 2;
}
