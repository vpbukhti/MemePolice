package main

import (
	"context"
	"log/slog"
	"time"
)

func (r *UpdateHandler) RunAutotopkek(ctx context.Context) error {
	tic := time.NewTicker(time.Minute * 10)
	defer tic.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case now := <-tic.C:
			// poor men's cron; runs +- every 10m of 09h
			if now.Hour() != 9 {
				continue
			}

			err := r.runAutotopkek(ctx, now)
			if err != nil {
				slog.ErrorContext(ctx, "unable to run autotopkek", slog.String("error", err.Error()))
			}
		}
	}
}

func (r *UpdateHandler) runAutotopkek(ctx context.Context, now time.Time) error {
	// get all autotopkek chats

	// for each run autotopkek
}

func (r *UpdateHandler) runAutotopkekForChat(ctx context.Context, now time.Time, chatID int64) error {
	// get current topkek state

	// no topkek - start topkek
	// running - if > 1 week old + last topkek message > 23h - attempt to finish; if finished - attempt to start new one (send service msg first to have the end-start reference)
	// finished - if > 1 week old - attempt to start (send service msg first to have the end-start reference)
}
