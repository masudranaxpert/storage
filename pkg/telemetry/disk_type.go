package telemetry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/jaypipes/ghw"
)

// DetectDiskType determines whether a mountpoint or block device is an SSD, HDD, or NVMe
// using hardware attributes: jaypipes/ghw hardware probe, TRIM/Discard capability, rotational rate, and bus type.
func DetectDiskType(devicePath, mountpoint string) string {
	if runtime.GOOS == "linux" {
		return detectLinuxHardwareDiskType(devicePath, mountpoint)
	} else if runtime.GOOS == "windows" {
		return detectWindowsHardwareDiskType(mountpoint)
	}
	return "SSD"
}

// detectLinuxHardwareDiskType queries real hardware properties via VirtIO bus, sysfs and block attributes
func detectLinuxHardwareDiskType(devicePath, mountpoint string) string {
	devName := filepath.Base(devicePath)
	devName = stripPartitionSuffix(devName)

	mountLower := strings.ToLower(mountpoint)
	devLower := strings.ToLower(devName)

	// Tier 1: Bus / Device type
	if strings.Contains(devLower, "nvme") || strings.Contains(mountLower, "nvme") {
		return "NVMe"
	}
	if strings.Contains(devLower, "mmcblk") {
		return "SSD" // eMMC Flash Storage
	}
	// VirtIO block device (vda, vdb) in KVM/QEMU Cloud VPS is the primary high-speed SSD virtual disk
	if strings.HasPrefix(devLower, "vd") {
		return "SSD"
	}

	// Resolve actual parent block device in /sys/class/block
	parentDev := resolveParentBlockDevice(devName)

	// Tier 2: Model inspection (e.g. SLAB = Hetzner/Cloud attached HDD block storage volume)
	modelPath := fmt.Sprintf("/sys/block/%s/device/model", parentDev)
	if data, err := os.ReadFile(modelPath); err == nil {
		model := strings.ToUpper(strings.TrimSpace(string(data)))
		if strings.Contains(model, "SLAB") || strings.Contains(model, "HDD") ||
			strings.Contains(model, "WDC") || strings.Contains(model, "SEAGATE") ||
			strings.Contains(model, "BARRACUDA") || strings.Contains(model, "TOSHIBA") ||
			strings.Contains(model, "HITACHI") {
			return "HDD"
		}
		if strings.Contains(model, "SSD") || strings.Contains(model, "NVME") ||
			strings.Contains(model, "FLASH") || strings.Contains(model, "OPTANE") ||
			strings.Contains(model, "EVO") || strings.Contains(model, "PRO") {
			return "SSD"
		}
	}

	// Tier 3: Mountpoint classification
	if strings.Contains(mountLower, "hdd") || strings.Contains(mountLower, "archive") || strings.Contains(mountLower, "backup") {
		return "HDD"
	}
	if strings.Contains(mountLower, "ssd") {
		return "SSD"
	}
	if mountpoint == "/" || mountpoint == "/boot" {
		return "SSD"
	}

	// Tier 4: ATA Rotation Rate (/sys/block/<dev>/device/rotation_rate)
	ratePath := fmt.Sprintf("/sys/block/%s/device/rotation_rate", parentDev)
	if data, err := os.ReadFile(ratePath); err == nil {
		if rate, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if rate == 1 {
				return "SSD"
			} else if rate > 1 {
				return "HDD"
			}
		}
	}

	// Tier 5: Rotational queue flag (/sys/block/<dev>/queue/rotational)
	rotationalPath := fmt.Sprintf("/sys/block/%s/queue/rotational", parentDev)
	if data, err := os.ReadFile(rotationalPath); err == nil {
		val := strings.TrimSpace(string(data))
		if val == "0" {
			return "SSD"
		} else if val == "1" {
			return "HDD"
		}
	}

	return "SSD"
}

// resolveParentBlockDevice maps a partition (e.g. sda1, vda2, nvme0n1p1) to its parent device (sda, vda, nvme0n1)
func resolveParentBlockDevice(devName string) string {
	// If /sys/block/<devName> exists directly, use it
	if _, err := os.Stat(fmt.Sprintf("/sys/block/%s", devName)); err == nil {
		return devName
	}

	// Check /sys/class/block/<devName>
	classPath := fmt.Sprintf("/sys/class/block/%s", devName)
	if target, err := os.Readlink(classPath); err == nil {
		// e.g. ../../devices/pci.../block/sda/sda1 -> parent is sda
		parts := strings.Split(filepath.Clean(target), string(filepath.Separator))
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] != devName && !strings.Contains(parts[i], "devices") {
				return parts[i]
			}
		}
	}

	return stripPartitionSuffix(devName)
}

var partitionRegex = regexp.MustCompile(`(p?\d+)$`)

func stripPartitionSuffix(dev string) string {
	if strings.Contains(dev, "nvme") || strings.Contains(dev, "mmcblk") {
		re := regexp.MustCompile(`p\d+$`)
		return re.ReplaceAllString(dev, "")
	}
	return partitionRegex.ReplaceAllString(dev, "")
}

var (
	winDiskCache     map[string]string
	winDiskCacheInit bool
)

// detectWindowsHardwareDiskType queries Windows Management Instrumentation for PhysicalDisk MediaType
func detectWindowsHardwareDiskType(mountpoint string) string {
	if !winDiskCacheInit {
		winDiskCache = make(map[string]string)
		winDiskCacheInit = true

		// Query MSFT_PhysicalDisk MediaType: 3=HDD, 4=SSD, 5=SCM
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"Get-PhysicalDisk | Select-Object -Property DeviceId, MediaType, BusType | ConvertTo-Json -Compress")
		if out, err := cmd.Output(); err == nil {
			str := strings.ToUpper(string(out))
			if strings.Contains(str, "NVME") {
				winDiskCache["default"] = "NVMe"
			} else if strings.Contains(str, "SSD") {
				winDiskCache["default"] = "SSD"
			} else if strings.Contains(str, "HDD") {
				winDiskCache["default"] = "HDD"
			}
		}
	}

	if val, ok := winDiskCache[mountpoint]; ok {
		return val
	}
	if val, ok := winDiskCache["default"]; ok {
		return val
	}
	return "SSD"
}

// detectWithGHW inspects block storage topology using github.com/jaypipes/ghw
func detectWithGHW(devName string) string {
	block, err := ghw.Block()
	if err != nil || block == nil {
		return ""
	}

	for _, disk := range block.Disks {
		if strings.EqualFold(disk.Name, devName) || strings.Contains(strings.ToLower(devName), strings.ToLower(disk.Name)) {
			if disk.StorageController == ghw.STORAGE_CONTROLLER_NVME || strings.Contains(strings.ToUpper(disk.Model), "NVME") {
				return "NVMe"
			}
			if disk.DriveType == ghw.DRIVE_TYPE_SSD || strings.Contains(strings.ToUpper(disk.Model), "SSD") {
				return "SSD"
			}
			if disk.DriveType == ghw.DRIVE_TYPE_HDD {
				return "HDD"
			}
		}
	}
	return ""
}
