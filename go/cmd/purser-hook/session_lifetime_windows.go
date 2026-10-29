//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"purser-hook/internal/waiter"
	"syscall"
)

func watchSessionOwner(ctx context.Context, owner waiter.Owner, done chan struct{}, stderr io.Writer) error {
	handle, err := waiter.OpenOwner(owner)
	if err != nil {
		return err
	}
	go func() {
		defer close(done)
		defer func() {
			if err := syscall.CloseHandle(handle); err != nil {
				fmt.Fprintf(stderr, "Purser: session owner monitor handle close failed: %v\n", err)
			}
		}()
		for ctx.Err() == nil {
			result, err := syscall.WaitForSingleObject(handle, 100)
			if err != nil {
				fmt.Fprintf(stderr, "Purser: session owner monitor wait failed: %v\n", err)
				return
			}
			if result == 0 {
				return
			}
			if result != 258 {
				fmt.Fprintf(stderr, "Purser: session owner monitor unexpected wait: %d\n", result)
				return
			}
		}
	}()
	return nil
}
