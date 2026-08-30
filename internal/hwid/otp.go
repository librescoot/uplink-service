package hwid

import (
	"encoding/hex"
	"os"
	"strings"
)

const (
	fslOTPCfg0 = "/sys/fsl_otp/HW_OCOTP_CFG0"
	fslOTPCfg1 = "/sys/fsl_otp/HW_OCOTP_CFG1"
	nvmemPath  = "/sys/bus/nvmem/devices/imx-ocotp0/nvmem"

	// i.MX6 CFG0/CFG1 offset in the raw OCOTP nvmem image.
	nvmemUIDOffset = 0x410
)

// Read fuse-shadow files first; use raw OCOTP when that driver is absent.
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
