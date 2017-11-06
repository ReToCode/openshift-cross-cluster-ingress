package balancer

import (
	"io"
	"net"
	"time"

	log "github.com/sirupsen/logrus"
)

const (

	/* Buffer size to handle data from socket */
	BUFFER_SIZE = 16 * 1024

	/* Interval of pushing aggregated read/write stats */
	PROXY_STATS_PUSH_INTERVAL = 1 * time.Second
)

/**
 * Perform copy/proxy data from 'from' to 'to' socket, counting r/w stats and
 * dropping connection if timeout exceeded
 */
func proxy(to net.Conn, from net.Conn, timeout time.Duration) <-chan ReadWriteCount {

	stats := make(chan ReadWriteCount)
	outStats := make(chan ReadWriteCount)

	rwcBuffer := ReadWriteCount{}
	ticker := time.NewTicker(PROXY_STATS_PUSH_INTERVAL)
	flushed := false

	// Stats collecting goroutine
	go func() {

		if timeout > 0 {
			from.SetReadDeadline(time.Now().Add(timeout))
		}

		for {
			select {
			case <-ticker.C:
				if !rwcBuffer.IsZero() {
					outStats <- rwcBuffer
				}
				flushed = true
			case rwc, ok := <-stats:

				if !ok {
					ticker.Stop()
					if !flushed && !rwcBuffer.IsZero() {
						outStats <- rwcBuffer
					}
					close(outStats)
					return
				}

				if timeout > 0 && rwc.CountRead > 0 {
					from.SetReadDeadline(time.Now().Add(timeout))
				}

				// Remove non blocking
				if flushed {
					rwcBuffer = rwc
				} else {
					rwcBuffer.CountWrite += rwc.CountWrite
					rwcBuffer.CountRead += rwc.CountRead
				}

				flushed = false
			}
		}
	}()

	// Run proxy copier
	go func() {
		err := Copy(to, from, stats)
		// hack to determine normal close. TODO: fix when it will be exposed in golang
		e, ok := err.(*net.OpError)
		if err != nil && (!ok || e.Err.Error() != "use of closed network connection") {
			log.Warn(err)
		}

		to.Close()
		from.Close()

		// Stop stats collecting goroutine
		close(stats)
	}()

	return outStats
}

/**
 * It's build by analogy of io.Copy
 */
func Copy(to io.Writer, from io.Reader, ch chan<- ReadWriteCount) error {

	buf := make([]byte, BUFFER_SIZE)
	var err error = nil

	for {
		readN, readErr := from.Read(buf)

		if readN > 0 {

			writeN, writeErr := to.Write(buf[0:readN])

			if writeN > 0 {
				ch <- ReadWriteCount{CountRead: uint(readN), CountWrite: uint(writeN)}
			}

			if writeErr != nil {
				err = writeErr
				break
			}

			if readN != writeN {
				err = io.ErrShortWrite
				break
			}
		}

		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			err = readErr
			break
		}
	}

	return err
}
