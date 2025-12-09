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
	tic := time.NewTicker(time.Minute)
	defer tic.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case now := <-tic.C:
			slog.InfoContext(ctx, "running autotopkek")

			err := r.runAutoTopkek(ctx, now.UTC())
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
			slog.WarnContext(ctx, "unable to create autotopkek",
				slog.String("err", err.Error()),
				slog.Int64("chat_id", chat.ChatID),
			)
			return nil
		}

	case lastTopkek.Status == TopkekStatusStarted:
		err := r.finishAutoTopkek(ctx, storage, now, chat, lastTopkek)
		if err != nil && !errors.Is(err, errFinishAutoTopkekPreconditionsNotMet) {
			return fmt.Errorf("unable to finish autotopkek: %w", err)
		}
		if err != nil && errors.Is(err, errFinishAutoTopkekPreconditionsNotMet) {
			slog.WarnContext(ctx, "unable to finish autotopkek",
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
	if now.Minute()%3 != 0 {
		return 0, fmt.Errorf("now is not the time: %w", errCreateAutoTopkekPreconditionsNotMet)
	}
	if !chat.IsAutoTopkek {
		return 0, fmt.Errorf("not autotopkek: %w", errCreateAutoTopkekPreconditionsNotMet)
	}
	if lastTopkek != nil &&
		lastTopkek.Status != TopkekStatusDone {
		return 0, fmt.Errorf("last topkek is not finished: %w", errCreateAutoTopkekPreconditionsNotMet)
	}
	if lastTopkek != nil &&
		// lastTopkek.CreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour*24*7))) {
		lastTopkek.CreatedAt.UTC().Truncate(time.Minute).After(now.Add(-time.Minute*3*2)) {
		return 0, fmt.Errorf("last topkek was too recent: %w", errCreateAutoTopkekPreconditionsNotMet)
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
			return 0, fmt.Errorf("no source messages: %w", errCreateAutoTopkekPreconditionsNotMet)
		}
		listMessagesOpts.StartingMessageID = firstMessageID
	}

	sourceMessages, err := storage.ListMessagesWithReactionCount(ctx, listMessagesOpts)
	if err != nil {
		return 0, fmt.Errorf("unable to find topkek source messages: %w", err)
	}
	if len(sourceMessages) < 2 {
		return 0, fmt.Errorf("not enough source messages: %w", errCreateAutoTopkekPreconditionsNotMet)
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
		return err
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

	slog.InfoContext(ctx, "autotopkek created",
		slog.Int64("chat_id", chat.ChatID),
	)

	return nil
}

var errFinishAutoTopkekPreconditionsNotMet = fmt.Errorf("finish autotopkek preconditions are not met")

func (r *UpdateHandler) checkFinishAutoTopkekPreconditions(ctx context.Context, storage Storage,
	now time.Time,
	chat ChatSettings,
	lastTopkek *Topkek,
) error {
	if now.Minute()%3 != 0 {
		return fmt.Errorf("now is not the time: %w", errFinishAutoTopkekPreconditionsNotMet)
	}
	if !chat.IsAutoTopkek {
		return fmt.Errorf("not autotopkek: %w", errFinishAutoTopkekPreconditionsNotMet)
	}
	if lastTopkek != nil &&
		lastTopkek.Status != TopkekStatusStarted {
		return fmt.Errorf("topkek not started: %w", errFinishAutoTopkekPreconditionsNotMet)
	}
	if lastTopkek != nil &&
		// lastTopkek.CreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour*24*7))) {
		lastTopkek.CreatedAt.UTC().Truncate(time.Minute).After(now.Add(-time.Minute*3*2)) {
		return fmt.Errorf("topkek started to recently: %w", errFinishAutoTopkekPreconditionsNotMet)
	}

	topkekMessages, err := storage.GetTopkekMessages(ctx, lastTopkek.ID)
	if err != nil {
		return fmt.Errorf("unable to get topkek messages: %w", err)
	}

	latestMessageAt := time.Time{}
	for _, message := range topkekMessages {
		if message.CreatedAt.After(latestMessageAt) {
			latestMessageAt = message.CreatedAt
		}
	}

	// if maxCreatedAt.Truncate(time.Hour).After(now.Add(-time.Hour * 23)) {
	if latestMessageAt.UTC().Truncate(time.Minute).After(now.Add(-time.Minute * 2)) {
		slog.InfoContext(ctx,
			"latest topkek messages are too recent",
			"latest_message_at",
			latestMessageAt.UTC().Truncate(time.Minute),
			"now",
			now.Add(-time.Minute*2),
		)
		return fmt.Errorf("latest topkek messages are too recent: %w", errFinishAutoTopkekPreconditionsNotMet)
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
		return err
	}

	err = r.finishTopkek(ctx, storage, lastTopkek.ID)
	if err != nil {
		return fmt.Errorf("unable to finish last topkek: %w", err)
	}

	slog.InfoContext(ctx, "autotopkek finished",
		slog.Int64("chat_id", chat.ChatID),
	)

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
