/* SPDX-License-Identifier: Apache-2.0 */
/*
 * Synthetic ARM32 component for make test-arm. It is cross-compiled with
 * arm-linux-gnueabihf-gcc and executed by TargetCheck under real qemu-arm.
 * It is integration evidence for the tool, not Wget or CVE evidence.
 *
 *   component abi           report the ABI this binary actually runs with
 *   component large-offset  parse 2147483648 with OFFSET_PARSER
 *
 * Build with -DOFFSET_PARSER=strtol to reproduce a 32-bit long limit, or
 * -DOFFSET_PARSER=strtoll for a 64-bit parser.
 */
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifndef OFFSET_PARSER
#error "define OFFSET_PARSER as strtol or strtoll"
#endif
#define STRINGIFY(x) #x
#define NAME(x) STRINGIFY(x)

/* A hard-float call passes doubles in VFP registers. */
static double __attribute__((noinline)) scale(double value, int factor)
{
	return value * factor;
}

static int abi(void)
{
	double scaled = scale(1.5, 3);

#if defined(__arm__) && defined(__ARM_EABI__) && defined(__ARM_PCS_VFP)
	printf("ARM32 EABI hard-float: long=%d bits, long long=%d bits, scale=%.1f\n",
	       (int)sizeof(long) * 8, (int)sizeof(long long) * 8, scaled);
	return sizeof(long) == 4 ? 0 : 3;
#else
	fprintf(stderr, "abi assertion failed: not built for ARM32 EABI hard-float (scale=%.1f)\n", scaled);
	return 3;
#endif
}

static int large_offset(void)
{
	const char *text = "2147483648";
	char *end;
	long long value;

	errno = 0;
	value = OFFSET_PARSER(text, &end, 10);
	if (errno == ERANGE || *end != '\0' || value != 2147483648LL) {
		fprintf(stderr, "large-offset assertion failed: %s(\"%s\") rejected a 64-bit offset (errno=%s)\n",
		        NAME(OFFSET_PARSER), text, errno == ERANGE ? "ERANGE" : "0");
		return 1;
	}
	printf("large-offset ok: %s(\"%s\") = %lld\n", NAME(OFFSET_PARSER), text, value);
	return 0;
}

int main(int argc, char **argv)
{
	if (argc == 2 && strcmp(argv[1], "abi") == 0)
		return abi();
	if (argc == 2 && strcmp(argv[1], "large-offset") == 0)
		return large_offset();
	fprintf(stderr, "usage: component abi|large-offset\n");
	return 2;
}
