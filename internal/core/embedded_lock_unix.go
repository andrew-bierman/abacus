//go:build unix

package core

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// tryFlock opens (creating) path and takes an exclusive or shared flock without blocking. ok is false
// when someone else holds a conflicting lock.
func tryFlock(path string, exclusive bool) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

func closeLock(f *os.File) {
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// lockWait takes the exclusive lock on path, polling for up to timeout. Waiting for another abacus
// that is preparing the same tools is normal; waiting forever is not.
func lockWait(path string, timeout time.Duration) (unlock func(), err error) {
	deadline := time.Now().Add(timeout)
	for {
		f, ok, err := tryFlock(path, true)
		if err != nil {
			return nil, fmt.Errorf("could not open the lock file %s: %s", path, plainWhy(err))
		}
		if ok {
			return func() { closeLock(f) }, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another abacus has been preparing the built-in tools for %s (lock file %s): wait for it to finish, or delete that lock file if no other abacus is running", timeout.Round(time.Second), path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

var (
	useMu   sync.Mutex
	useHeld = map[string]*os.File{}
)

// holdInUse takes a shared lock on path for the life of the process (never released: the process
// ending releases it), the signal other abacus processes read as "do not remove this version".
func holdInUse(path string) {
	useMu.Lock()
	defer useMu.Unlock()
	if _, ok := useHeld[path]; ok {
		return
	}
	if f, ok, _ := tryFlock(path, false); ok {
		useHeld[path] = f
	}
}

// interruptContext cancels on SIGINT or SIGTERM, so an extraction can remove its partial file.
// done stops listening and, when a signal arrived, delivers it again with the default action: the
// process ends as it would have without the cleanup.
func interruptContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	var got os.Signal
	var mu sync.Mutex
	stop := make(chan struct{})
	go func() {
		select {
		case sg := <-ch:
			mu.Lock()
			got = sg
			mu.Unlock()
			cancel()
		case <-stop:
		}
	}()
	return ctx, func() {
		close(stop)
		signal.Stop(ch)
		cancel()
		mu.Lock()
		sg := got
		mu.Unlock()
		if sg != nil {
			signal.Reset(syscall.SIGINT, syscall.SIGTERM)
			_ = syscall.Kill(os.Getpid(), sg.(syscall.Signal))
		}
	}
}
