// Package ports decides which host port a sandbox is published on.
//
// The choice is deterministic: the same pull request in the same
// repository gets the same port every time. That is what makes a
// bookmarked tab still work tomorrow, and what lets a reviewer go back
// to a sandbox without looking the port up again.
package ports

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// The range pit publishes into. It sits above the ports developers
// reach for by habit -- 3000, 5173, 8080 -- and below the ephemeral
// range the kernel hands out for outgoing connections, so pit neither
// collides with a colleague's dev server nor with the operating system.
const (
	Min  = 40000
	Max  = 49999
	Span = Max - Min + 1
)

// Assignment is the outcome of choosing a port.
type Assignment struct {
	// Port is the port to publish on.
	Port int
	// Preferred is the port the pull request would have had if nothing
	// were in the way.
	Preferred int
}

// Moved reports whether the preferred port was unavailable. Worth
// telling the reviewer: it explains why today's URL differs from
// yesterday's.
func (a Assignment) Moved() bool { return a.Port != a.Preferred }

// Preferred computes the port a pull request should get. repoKey is any
// stable identifier for the repository; the same string must always
// produce the same port, so the canonical identity belongs here rather
// than a filesystem path.
func Preferred(repoKey string, pr int) int {
	sum := sha256.Sum256([]byte(repoKey))
	base := binary.BigEndian.Uint32(sum[:4])

	// The repository decides where in the range its reviews start; the
	// pull request number walks upwards from there. Consecutive pull
	// requests therefore get consecutive ports, which is easy to
	// recognise and never collides within one repository.
	return Min + int((base+uint32(pr))%Span) //nolint:gosec // the modulo keeps this inside the range
}

// Taken reports whether a port is already in use. It is the default
// test used by Reserve.
func Taken(ctx context.Context, port int) bool {
	// Listening on every interface is the strict test: Docker publishes
	// on 0.0.0.0, which collides with a loopback-only listener too.
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

// Reserve picks a usable port, starting from the preferred one and
// walking upwards until it finds a free one. It wraps at the top of the
// range, so a busy machine still gets an answer rather than an error
// about the last few ports.
//
// taken may be nil, in which case the port is tested by binding it. A
// caller that knows about ports pit itself has handed out should pass a
// predicate covering those as well: two sandboxes must not race for the
// same number between the check and Docker actually binding it.
func Reserve(ctx context.Context, repoKey string, pr int, taken func(int) bool) (Assignment, error) {
	if taken == nil {
		taken = func(p int) bool { return Taken(ctx, p) }
	}

	preferred := Preferred(repoKey, pr)
	for i := range Span {
		port := Min + (preferred-Min+i)%Span
		if !taken(port) {
			return Assignment{Port: port, Preferred: preferred}, nil
		}
	}

	return Assignment{}, errs.New("every port between %d and %d is in use", Min, Max).
		WithHint("stop some sandboxes with `pit down --all`, or free up ports")
}

// WaitFree waits for a port to become free, which is what makes
// restarting a sandbox immediately after stopping it work: the kernel
// holds a closed listener in TIME_WAIT for a moment.
func WaitFree(ctx context.Context, port int, within time.Duration) error {
	const poll = 50 * time.Millisecond

	deadline := time.Now().Add(within)
	for {
		if !Taken(ctx, port) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errs.New("port %d is still in use after %v", port, within).
				WithHint("find out what holds it with `lsof -i :%d`", port)
		}

		// Waiting has to end the moment the user presses Ctrl+C, not
		// at the end of the next poll.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// String renders an assignment for a log line.
func (a Assignment) String() string {
	if !a.Moved() {
		return strconv.Itoa(a.Port)
	}
	return fmt.Sprintf("%d (%d was in use)", a.Port, a.Preferred)
}
