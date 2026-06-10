package handler

import (
	"fmt"
	"strconv"
)

func intToStr(i int) string {
	return strconv.Itoa(i)
}

func formatURL(base, path string) string {
	return fmt.Sprintf("%s%s", base, path)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
