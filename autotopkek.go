package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

func (r *UpdateHandler) RunAutotopkek(ctx context.Context) error {
	// tic := time.NewTicker(time.Minute * 10)
	tic := time.NewTicker(time.Minute * 2)
	defer tic.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case now := <-tic.C:
			// poor men's cron; runs +- every 10m of 09h
			// if now.Hour() != 9 {
			if now.Minute()%6 == 0 {
				continue
			}

			err := r.runAutoTopkek(ctx, now)
			if err != nil {
				slog.ErrorContext(ctx, "unable to run autotopkek", slog.String("error", err.Error()))
			}
		}
	}
}

func (r *UpdateHandler) runAutoTopkek(ctx context.Context, now time.Time) error {
	chatIDs, err := r.storage.ListChatsWithAutotopkek(ctx)
	if err != nil {
		return fmt.Errorf("unable to list chats with autotopkek: %w", err)
	}

	for _, chat := range chatIDs {
		err := r.storage.ExecWithTx(ctx, func(ctx context.Context, storage Storage) error {
			err := r.runAutoTopkekForChat(ctx, storage, now, chat)
			if err != nil {
				return fmt.Errorf("unable to run autotopkek for chat(%d): %w", chat.ChatID, err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("unable to exec in tx: %w", err)
		}
	}

	return nil
}

func (r *UpdateHandler) runAutoTopkekForChat(ctx context.Context, storage Storage, now time.Time, chat ChatSettings) error {
	lastTopkek, err := storage.GetLastTopkek(ctx, chat.ChatID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get last topkek: %w", err)
	}

	switch {
	case lastTopkek == nil || lastTopkek.Status == TopkekStatusDone:
		err := r.createAutoTopkek(ctx, storage, now, chat, lastTopkek)
		if err != nil && !errors.Is(err, errCreateAutoTopkekPreconditionsNotMet) {
			return fmt.Errorf("unable to create autotopkek: %w", err)
		}
		if err != nil && errors.Is(err, errCreateAutoTopkekPreconditionsNotMet) {
			slog.WarnContext(ctx, "create autotopkek preconditions are not met",
				slog.String("err", err.Error()),
				slog.Int64("chat_id", chat.ChatID),
			)
			return nil
		}

	case lastTopkek.Status == TopkekStatusStarted:
		err := r.finishAutoTopkek(ctx, storage, now, chat, lastTopkek)
		if err != nil && !errors.Is(err, errFinishAutoTopkekPreconditionsNotMet) {
			return fmt.Errorf("unable to create autotopkek: %w", err)
		}
		if err != nil && errors.Is(err, errFinishAutoTopkekPreconditionsNotMet) {
			slog.WarnContext(ctx, "finish autotopkek preconditions are not met",
				slog.String("err", err.Error()),
				slog.Int64("chat_id", chat.ChatID),
			)
			return nil
		}

	default:
		slog.WarnContext(ctx, "topkek is in the unknown state",
			slog.String("status", string(lastTopkek.Status)),
			slog.Int64("topkek_id", lastTopkek.ID),
			slog.Int64("chat_id", chat.ChatID),
		)
	}

	return nil
}

var errCreateAutoTopkekPreconditionsNotMet = fmt.Errorf("create autotopkek preconditions are not met")

func (r *UpdateHandler) checkCreateAutoTopkekPreconditions(ctx context.Context, storage Storage,
	now time.Time,
	chat ChatSettings,
	lastTopkek *Topkek,
) (int, error) {
	if !chat.IsAutoTopkek {
		return 0, errCreateAutoTopkekPreconditionsNotMet
	}
	if lastTopkek != nil &&
		(lastTopkek.Status != TopkekStatusDone ||
			// lastTopkek.CreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour*24*7))) {
			lastTopkek.CreatedAt.Truncate(time.Minute*6).After(now.Add(-time.Minute*6*2))) {
		return 0, errCreateAutoTopkekPreconditionsNotMet
	}

	listMessagesOpts := ListMessagesWithReactionCountOptions{
		ChatID:           chat.ChatID,
		MinReactions:     chat.MinReactions,
		ExcludeReactions: ExcludeReactions,
	}
	if lastTopkek != nil {
		listMessagesOpts.StartingMessageID = lastTopkek.MessageID
	} else {
		firstMessageID, err := storage.GetFirstChatMessageID(ctx, chat.ChatID)
		if err != nil && !errors.Is(err, &ErrNotFound{}) {
			return 0, fmt.Errorf("unable to get first chat message ID: %w", err)
		}
		if err != nil && errors.Is(err, &ErrNotFound{}) {
			// no messages in the chat, can't start the first topkek
			return 0, errCreateAutoTopkekPreconditionsNotMet
		}
		listMessagesOpts.StartingMessageID = firstMessageID
	}

	sourceMessages, err := storage.ListMessagesWithReactionCount(ctx, listMessagesOpts)
	if err != nil {
		return 0, fmt.Errorf("unable to find topkek source messages: %w", err)
	}
	if len(sourceMessages) < 2 {
		return 0, errCreateAutoTopkekPreconditionsNotMet
	}

	return listMessagesOpts.StartingMessageID, nil
}

func (r *UpdateHandler) createAutoTopkek(ctx context.Context, storage Storage,
	now time.Time,
	chat ChatSettings,
	lastTopkek *Topkek,
) error {
	startMessageID, err := r.checkCreateAutoTopkekPreconditions(ctx, storage, now, chat, lastTopkek)
	if err != nil {
		return fmt.Errorf("autotopkek preconditions are not met: %w", err)
	}

	topkekMessage, err := r.sendMessage(ctx, chat.ChatID, "/topkek@"+r.bot.Self.UserName)
	if err != nil {
		return fmt.Errorf("unable to send fake topkek message: %w", err)
	}

	createOpts := createTopkekOptions{
		MessageID:         topkekMessage.MessageID,
		ChatID:            chat.ChatID,
		Name:              defaultTopkekName(),
		AuthorID:          r.bot.Self.ID,
		StartingMessageID: &startMessageID,
		MinReactions:      chat.MinReactions,
	}

	err = r.createTopkek(ctx, storage, createOpts)
	if err != nil {
		return fmt.Errorf("unable to create topkek: %w", err)
	}

	return nil
}

var errFinishAutoTopkekPreconditionsNotMet = fmt.Errorf("finish autotopkek preconditions are not met")

func (r *UpdateHandler) checkFinishAutoTopkekPreconditions(ctx context.Context, storage Storage,
	now time.Time,
	chat ChatSettings,
	lastTopkek *Topkek,
) error {
	if !chat.IsAutoTopkek {
		return errFinishAutoTopkekPreconditionsNotMet
	}
	if lastTopkek != nil &&
		(lastTopkek.Status != TopkekStatusStarted ||
			// lastTopkek.CreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour*24*7))) {
			lastTopkek.CreatedAt.Truncate(time.Minute*6).After(now.Add(-time.Minute*6*2))) {
		return errFinishAutoTopkekPreconditionsNotMet
	}

	topkekMessages, err := storage.GetTopkekMessages(ctx, lastTopkek.ID)
	if err != nil {
		return fmt.Errorf("unable to get topkek messages: %w", err)
	}

	maxCreatedAt := time.Time{}
	for _, message := range topkekMessages {
		if message.CreatedAt.After(maxCreatedAt) {
			maxCreatedAt = message.CreatedAt
		}
	}

	// if maxCreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour * 23)) {
	if maxCreatedAt.Truncate(time.Minute * 6).After(now.Add(-time.Minute * 3)) {
		return errFinishAutoTopkekPreconditionsNotMet
	}

	return nil
}

func (r *UpdateHandler) finishAutoTopkek(ctx context.Context, storage Storage,
	now time.Time,
	chat ChatSettings,
	lastTopkek *Topkek,
) error {
	// check if we should
	err := r.checkFinishAutoTopkekPreconditions(ctx, storage, now, chat, lastTopkek)
	if err != nil {
		return fmt.Errorf("finish autotopkek preconditons are not met: %w", err)
	}

	err = r.finishTopkek(ctx, storage, lastTopkek.ID)
	if err != nil {
		return fmt.Errorf("unable to finish last topkek: %w", err)
	}

	lastTopkek, err = storage.GetLastTopkek(ctx, chat.ChatID)
	if err != nil {
		return fmt.Errorf("unable to get last topkek: %w", err)
	}
	if lastTopkek.Status == TopkekStatusStarted {
		return nil
	}

	// try immediately starting new topkek

	err = r.createAutoTopkek(ctx, storage, now, chat, lastTopkek)
	if err != nil {
		// do return err to try commiting the tx anyway
		slog.WarnContext(ctx, "unable to start new topkek",
			slog.String("err", err.Error()),
			slog.Int64("chat_id", chat.ChatID),
		)
		return nil
	}

	return nil
}
