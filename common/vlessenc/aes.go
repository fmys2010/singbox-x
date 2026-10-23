package vlessenc

import (
	"runtime"

	"golang.org/x/sys/cpu"
)

// HasAESGCMHardwareSupport mirrors the check of crypto/cipher's AES-GCM fast path,
// it decides between AES-GCM and ChaCha20-Poly1305 for the client's records.
var HasAESGCMHardwareSupport = (runtime.GOARCH == "amd64" && cpu.X86.HasAES && cpu.X86.HasPCLMULQDQ && cpu.X86.HasSSE41 && cpu.X86.HasSSSE3) ||
	(runtime.GOARCH == "arm64" && cpu.ARM64.HasAES && cpu.ARM64.HasPMULL) ||
	(runtime.GOARCH == "s390x" && cpu.S390X.HasAES && cpu.S390X.HasAESCTR && cpu.S390X.HasGHASH) ||
	runtime.GOARCH == "ppc64" || runtime.GOARCH == "ppc64le"
