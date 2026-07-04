// Package hwid reads immutable board identifiers from the i.MX on-chip OTP
// (one-time-programmable) fuses. All reads degrade gracefully to an empty
// string when the sysfs entries are absent (e.g. when running off target).
package hwid

import (
	"encoding/hex"
	"os"
	"strings"
)

// Named fuse-shadow files exposed by the fsl_otp driver.
const (
	fslOTPCfg0 = "/sys/fsl_otp/HW_OCOTP_CFG0"
	fslOTPCfg1 = "/sys/fsl_otp/HW_OCOTP_CFG1"
	nvmemPath  = "/sys/bus/nvmem/devices/imx-ocotp0/nvmem"

	// Byte offset of the CFG0 fuse word within the raw OCOTP nvmem blob on
	// i.MX6. CFG0/CFG1 together form the chip unique ID. Used only for the
	// nvmem fallback path.
	nvmemUIDOffset = 0x410
)

// BoardSerial returns a hex-encoded board serial derived from the OCOTP unique
// ID, or "" if it cannot be read.
func BoardSerial() string {
	if s := fromFSLOTP(); s != "" {
		return s
	}
	return fromNVMEM()
}

func fromFSLOTP() string {
	c0 := readTrim(fslOTPCfg0)
	c1 := readTrim(fslOTPCfg1)
	if c0 == "" || c1 == "" {
		return ""
	}
	// The shadow files are of the form "0x1234abcd"; concatenate the two words.
	return strings.TrimPrefix(c0, "0x") + strings.TrimPrefix(c1, "0x")
}

func fromNVMEM() string {
	f, err := os.Open(nvmemPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, 8)
	n, err := f.ReadAt(buf, nvmemUIDOffset)
	if err != nil || n < 8 {
		return ""
	}
	return hex.EncodeToString(buf)
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
