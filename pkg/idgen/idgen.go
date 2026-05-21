package idgen

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	TracePrefix = "trc_"
	TaskPrefix  = "task_"
)

func NewTraceID() string {
	return TracePrefix + stripHyphens(uuid.New().String())[:12]
}

func NewTaskID() string {
	return TaskPrefix + stripHyphens(uuid.New().String())[:12]
}

func NewSessionID() string {
	return fmt.Sprintf("sess_%s", stripHyphens(uuid.New().String())[:12])
}

func stripHyphens(s string) string {
	return strings.ReplaceAll(s, "-", "")
}
