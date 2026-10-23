package v2rayxhttp

import (
	"bytes"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBufferedPipe(t *testing.T) {
	reader, writer := newBufferedPipe(1000)
	payload := make([]byte, 1<<20)
	rand.Read(payload)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for rest := payload; len(rest) > 0; {
			n := min(len(rest), 777)
			_, err := writer.Write(rest[:n])
			require.NoError(t, err)
			rest = rest[n:]
		}
		writer.Close()
	}()
	received, err := io.ReadAll(reader)
	require.NoError(t, err)
	wg.Wait()
	require.True(t, bytes.Equal(payload, received))
}

func TestBufferedPipeReaderClose(t *testing.T) {
	reader, writer := newBufferedPipe(10)
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write(make([]byte, 100)) // blocks, the buffer holds 10 bytes
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	reader.CloseWithError(io.ErrUnexpectedEOF)
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	case <-time.After(2 * time.Second):
		t.Fatal("writer was not released")
	}
	_, err := reader.Read(make([]byte, 1))
	require.Error(t, err)
}
