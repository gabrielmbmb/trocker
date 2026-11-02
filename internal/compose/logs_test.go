package compose

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentNativeLogCallbacks(t *testing.T) {
	var output bytes.Buffer
	logs := composeLogs(t.Context(), &output, false)
	var group sync.WaitGroup
	for i := range 64 {
		group.Go(func() {
			name := "service-" + strconv.Itoa(i)
			logs.Log(name, "application output")
			logs.Err(name, "application stderr")
			logs.Status(name, "exited")
		})
	}
	group.Wait()
	if strings.Count(output.String(), "\n") != 192 || strings.Contains(output.String(), "\x1b") {
		t.Fatal("native log callbacks lost output or emitted terminal escapes")
	}
}
