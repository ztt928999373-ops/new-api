// Copyright (c) QuantumNous and contributors.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConcurrentLoggingAndSinglePendingRotation(t *testing.T) {
	directory := t.TempDir()
	oldDirectory := *common.LogDir
	common.LogWriterMu.Lock()
	oldWriter, oldError := gin.DefaultWriter, gin.DefaultErrorWriter
	common.LogWriterMu.Unlock()
	logStateMu.Lock()
	oldCount, oldWorking := logCount, setupLogWorking
	logStateMu.Unlock()
	currentLogPathMu.RLock()
	oldPath, oldFile := currentLogPath, currentLogFile
	currentLogPathMu.RUnlock()
	require.Nil(t, oldFile, "fixture must not close a previous active logger")
	pathLocked := false
	t.Cleanup(func() {
		if pathLocked {
			currentLogPathMu.Unlock()
		}
		require.Eventually(t, func() bool { logStateMu.Lock(); defer logStateMu.Unlock(); return !setupLogWorking }, 3*time.Second, time.Millisecond, "rotation must finish before restoring the fixture")
		common.LogWriterMu.Lock()
		gin.DefaultWriter, gin.DefaultErrorWriter = oldWriter, oldError
		currentLogPathMu.Lock()
		if currentLogFile != nil {
			currentLogFile.Close()
		}
		currentLogPath, currentLogFile = oldPath, oldFile
		currentLogPathMu.Unlock()
		common.LogWriterMu.Unlock()
		*common.LogDir = oldDirectory
		logStateMu.Lock()
		logCount, setupLogWorking = oldCount, oldWorking
		logStateMu.Unlock()
	})
	*common.LogDir = directory
	SetupLogger()
	// A real publication lock is the barrier: rotation has acquired its setup
	// lock and opened the file, but cannot publish/close the old writer yet.
	currentLogPathMu.Lock()
	pathLocked = true
	logStateMu.Lock()
	logCount = maxLogCount
	logStateMu.Unlock()
	LogInfo(nil, "synthetic-rotation-trigger")
	require.Eventually(t, func() bool {
		if setupLogLock.TryLock() {
			setupLogLock.Unlock()
			return false
		}
		return true
	}, 3*time.Second, time.Millisecond, "rotation must reach the held publication barrier")
	// A competing manual setup must neither rotate twice nor clear the true
	// owner's pending flag. High count simulates logs arriving during rotation.
	SetupLogger()
	logStateMu.Lock()
	pending := setupLogWorking
	logCount = maxLogCount + 1
	logStateMu.Unlock()
	require.True(t, pending)
	const writers = 32
	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() { <-start; LogInfo(nil, fmt.Sprintf("synthetic-rotation-record-%03d", i)) })
	}
	go func() { wg.Wait(); close(done) }()
	close(start)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("writers blocked while rotation waited on publication")
	}
	logStateMu.Lock()
	count, stillPending := logCount, setupLogWorking
	logStateMu.Unlock()
	require.True(t, stillPending)
	require.Equal(t, maxLogCount+1+writers, count, "no second trigger may reset the count during the pending rotation")
	currentLogPathMu.Unlock()
	pathLocked = false
	require.Eventually(t, func() bool { logStateMu.Lock(); defer logStateMu.Unlock(); return !setupLogWorking }, 3*time.Second, time.Millisecond)
	files, err := filepath.Glob(filepath.Join(directory, "oneapi-*.log"))
	require.NoError(t, err)
	var contents strings.Builder
	for _, file := range files {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		contents.Write(b)
	}
	require.Equal(t, 1, strings.Count(contents.String(), "synthetic-rotation-trigger"))
	for i := range writers {
		require.Equal(t, 1, strings.Count(contents.String(), fmt.Sprintf("synthetic-rotation-record-%03d", i)), "each log must survive exactly once")
	}
}
