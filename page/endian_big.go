//go:build armbe || arm64be || m68k || mips || mips64 || mips64p32 || ppc || ppc64 || s390 || s390x || shbe || sparc || sparc64

package page

// The page format is little-endian and header fields are read as whole native
// 64-bit words. On a big-endian CPU every field would be scrambled, so refuse to build.
const _ = uint(-1) // page: big-endian CPUs are not supported
