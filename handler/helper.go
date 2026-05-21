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
