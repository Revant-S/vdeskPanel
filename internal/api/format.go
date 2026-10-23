package api

import "fmt"

// FormatBytes prints a size in decimal units, as file managers do.
func FormatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(n), 0
	for value >= 1000 && unit < len(units)-1 {
		value, unit = value/1000, unit+1
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.0f %s", value, units[unit])
}
