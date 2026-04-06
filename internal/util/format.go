package util

import "fmt"

func FormatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	// 单位后缀
	suffix := []string{"KB", "MB", "GB", "TB", "PB", "EB"}

	// 使用 float64 进行除法以保留两位小数
	return fmt.Sprintf("%.2f %s", float64(bytes)/float64(div), suffix[exp])
}
