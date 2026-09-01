package db

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const (
	peMachineI386  uint16 = 0x014c
	peMachineAmd64 uint16 = 0x8664
	peMachineArm64 uint16 = 0xaa64

	peDOSHeaderMinSize = 0x40
	peHeaderOffsetAddr = 0x3c
	peSignatureSize    = 4
	peCOFFHeaderSize   = 20
)

type driverAgentArchMismatchError struct {
	fileLabel    string
	processLabel string
}

func (e *driverAgentArchMismatchError) Error() string {
	if e == nil {
		return "driver agent architecture is incompatible"
	}
	return fmt.Sprintf("driver agent architecture is incompatible (file=%s, current process=%s)", e.fileLabel, e.processLabel)
}

func windowsMachineLabel(machine uint16) string {
	switch machine {
	case peMachineI386:
		return "windows-386"
	case peMachineAmd64:
		return "windows-amd64"
	case peMachineArm64:
		return "windows-arm64"
	default:
		return fmt.Sprintf("windows-unknown(0x%04x)", machine)
	}
}

func expectedWindowsMachineForGoArch(goarch string) (uint16, string, bool) {
	switch strings.ToLower(strings.TrimSpace(goarch)) {
	case "386":
		return peMachineI386, "windows-386", true
	case "amd64":
		return peMachineAmd64, "windows-amd64", true
	case "arm64":
		return peMachineArm64, "windows-arm64", true
	default:
		return 0, "", false
	}
}

func validateWindowsExecutableMachine(pathText string) error {
	return validateWindowsExecutableMachineForArch(pathText, runtime.GOARCH)
}

func validateWindowsExecutableMachineForArch(pathText string, goarch string) error {
	machine, err := readWindowsExecutableMachine(pathText)
	if err != nil {
		return fmt.Errorf("无法识别为有效的 Windows 可执行文件：%w", err)
	}

	expectedMachine, expectedLabel, ok := expectedWindowsMachineForGoArch(goarch)
	if !ok {
		return nil
	}
	if machine != expectedMachine {
		return &driverAgentArchMismatchError{
			fileLabel:    windowsMachineLabel(machine),
			processLabel: expectedLabel,
		}
	}
	return nil
}

func readWindowsExecutableMachine(pathText string) (uint16, error) {
	file, err := os.Open(pathText)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	info, statErr := file.Stat()
	if statErr != nil {
		return 0, statErr
	}
	if info.IsDir() {
		return 0, fmt.Errorf("路径是目录")
	}
	if info.Size() < peDOSHeaderMinSize {
		return 0, fmt.Errorf("文件头不完整")
	}

	var dosMagic [2]byte
	if err := readWindowsPEBytes(file, 0, dosMagic[:]); err != nil {
		return 0, fmt.Errorf("读取 DOS 头失败：%w", err)
	}
	if dosMagic[0] != 'M' || dosMagic[1] != 'Z' {
		return 0, fmt.Errorf("缺少 MZ 头")
	}

	var offsetBytes [4]byte
	if err := readWindowsPEBytes(file, peHeaderOffsetAddr, offsetBytes[:]); err != nil {
		return 0, fmt.Errorf("读取 PE 头偏移失败：%w", err)
	}
	peOffset := int64(binary.LittleEndian.Uint32(offsetBytes[:]))
	if peOffset < peDOSHeaderMinSize {
		return 0, fmt.Errorf("PE 头偏移异常")
	}
	if peOffset+peSignatureSize+peCOFFHeaderSize > info.Size() {
		return 0, fmt.Errorf("PE 头不完整")
	}

	var signature [4]byte
	if err := readWindowsPEBytes(file, peOffset, signature[:]); err != nil {
		return 0, fmt.Errorf("读取 PE 签名失败：%w", err)
	}
	if signature[0] != 'P' || signature[1] != 'E' || signature[2] != 0 || signature[3] != 0 {
		return 0, fmt.Errorf("缺少 PE 签名")
	}

	var machineBytes [2]byte
	if err := readWindowsPEBytes(file, peOffset+peSignatureSize, machineBytes[:]); err != nil {
		return 0, fmt.Errorf("读取 PE 架构失败：%w", err)
	}
	return binary.LittleEndian.Uint16(machineBytes[:]), nil
}

func readWindowsPEBytes(reader io.ReaderAt, offset int64, target []byte) error {
	if len(target) == 0 {
		return nil
	}
	_, err := reader.ReadAt(target, offset)
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// ValidateOptionalDriverAgentExecutable 校验可选驱动代理二进制是否可在当前进程中执行。
// 当前主要用于 Windows 下的 PE 架构兼容性校验，避免升级后复用到错误架构的旧代理。
func ValidateOptionalDriverAgentExecutable(driverType string, executablePath string) error {
	pathText := strings.TrimSpace(executablePath)
	if pathText == "" {
		return fmt.Errorf("%s 驱动代理路径为空", driverDisplayName(driverType))
	}
	if runtime.GOOS != "windows" {
		return nil
	}
	if err := validateWindowsExecutableMachine(pathText); err != nil {
		return fmt.Errorf("%s 驱动代理不可用：%w", driverDisplayName(driverType), err)
	}
	return nil
}
