package logcollector

import "io"

// syncPipe returns a synchronous in-memory pipe suitable for bridging
// stdcopy.StdCopy output into a bufio.Scanner.
func syncPipe() (*io.PipeReader, *io.PipeWriter) {
	return io.Pipe()
}
