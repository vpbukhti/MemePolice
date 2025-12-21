package main

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tg "github.com/OvyFlash/telegram-bot-api"
	"go.uber.org/multierr"
)

func (r *UpdateHandler) sendMessage(ctx context.Context,
	chatID int64,
	text string,
) (*tg.Message, error) {
	voice := tg.NewMessage(chatID, text)

	res, err := r.bot.Send(voice)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &ErrNotFound{
				Err: fmt.Errorf("unable to send text message: %w", err),
			}
		}
		return nil, fmt.Errorf("unable to send text message: %w", err)
	}

	return &res, nil
}

func (r *UpdateHandler) sendVoiceMessage(ctx context.Context,
	chatID int64,
	name string,
	data []byte,
) error {
	voice := tg.NewVoice(chatID, tg.FileBytes{
		Name:  name,
		Bytes: data,
	})

	_, err := r.bot.Send(voice)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return &ErrNotFound{
				Err: fmt.Errorf("unable to send text message: %w", err),
			}
		}
		return fmt.Errorf("unable to send audio message: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendReaction(ctx context.Context, storage Storage, chatID int64, messageID int, emoji string) error {
	reactions := []tg.ReactionType{
		{
			Type:  "emoji",
			Emoji: emoji,
		},
	}

	err := storage.UpsertMessageReactions(ctx, MessageReactions{
		ChatID:    chatID,
		MessageID: messageID,
		UserID:    r.bot.Self.ID,
		Reactions: reactions,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to save stale meme reaction: %w", err)
	}

	reaction := tg.NewSetMessageReaction(chatID, messageID, reactions, false)
	_, err = r.bot.Send(reaction)
	if err != nil && !strings.Contains(err.Error(), "cannot unmarshal bool into Go value of type tgbotapi.Message") {
		return fmt.Errorf("unable to make send reaction: %w", err)
	}

	return nil
}

func (r *UpdateHandler) unsendReaction(ctx context.Context, storage Storage, chatID int64, messageID int) error {
	reactions := []tg.ReactionType{
		{
			Type:  "emoji",
			Emoji: OKEmoji,
		},
	}

	err := storage.UpsertMessageReactions(ctx, MessageReactions{
		ChatID:    chatID,
		MessageID: messageID,
		UserID:    r.bot.Self.ID,
		Reactions: []tg.ReactionType{},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to unsave stale meme reaction: %w", err)
	}

	reaction := tg.NewSetMessageReaction(chatID, messageID, reactions, false)

	_, err = r.bot.Send(reaction)
	if err != nil && !strings.Contains(err.Error(), "cannot unmarshal bool into Go value of type tgbotapi.Message") {
		return fmt.Errorf("unable to make unsend reaction: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendVoiceMessageReply(ctx context.Context,
	chatID int64,
	replyToMessageID int,
	name string,
	data []byte,
) error {
	voice := tg.NewVoice(chatID, tg.FileBytes{
		Name:  name,
		Bytes: data,
	})
	voice.ReplyParameters = tg.ReplyParameters{
		MessageID: replyToMessageID,
	}

	_, err := r.bot.Send(voice)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return &ErrNotFound{
				Err: fmt.Errorf("unable to send text message: %w", err),
			}
		}
		return fmt.Errorf("unable to send audio message: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendMessageReply(ctx context.Context,
	chatID int64,
	replyToMessageID int,
	text string,
) (int, error) {
	voice := tg.NewMessage(chatID, text)
	voice.ReplyParameters = tg.ReplyParameters{
		MessageID: replyToMessageID,
	}

	msg, err := r.bot.Send(voice)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return 0, &ErrNotFound{
				Err: fmt.Errorf("unable to send text message: %w", err),
			}
		}
		return 0, fmt.Errorf("unable to send text message: %w", err)
	}

	return msg.MessageID, nil
}

func (r *UpdateHandler) sendPhotoRepy(ctx context.Context,
	chatID int64,
	replyMessageID int,
	text string,
	fileID string,
) (*tg.Message, error) {
	photo := tg.NewPhoto(chatID, tg.FileID(fileID))
	photo.Caption = text
	photo.ReplyParameters = tg.ReplyParameters{
		MessageID: replyMessageID,
	}

	msg, err := r.bot.Send(photo)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &ErrNotFound{
				Err: fmt.Errorf("unable to send photo reply: %w", err),
			}
		}
		return nil, fmt.Errorf("unable to send photo reply: %w", err)
	}

	return &msg, nil
}

func (r *UpdateHandler) sendVideoRepy(ctx context.Context,
	chatID int64,
	replyMessageID int,
	text string,
	fileID string,
) (*tg.Message, error) {
	photo := tg.NewVideo(chatID, tg.FileID(fileID))
	photo.Caption = text
	photo.ReplyParameters = tg.ReplyParameters{
		MessageID: replyMessageID,
	}

	msg, err := r.bot.Send(photo)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &ErrNotFound{
				Err: fmt.Errorf("unable to send video reply: %w", err),
			}
		}
		return nil, fmt.Errorf("unable to send video reply: %w", err)
	}

	return &msg, nil
}

func (r *UpdateHandler) getTelegramImage(ctx context.Context, fileID string) (image.Image, error) {
	fileReader, err := r.getTelegramFile(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("unable to get telegram file: %w", err)
	}
	defer fileReader.Close()

	img, _, err := image.Decode(fileReader)
	if err != nil {
		return nil, fmt.Errorf("unable to decode image: %w", err)
	}

	return img, nil
}

func (r *UpdateHandler) getTelegramVideo(ctx context.Context, fileID, filePath string) error {
	fileReader, err := r.getTelegramFile(ctx, fileID)
	if err != nil {
		return fmt.Errorf("unable to get telegram file: %w", err)
	}
	defer fileReader.Close()

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("unable to open file: %w", err)
	}

	_, err = io.Copy(file, fileReader)
	if err != nil {
		return fmt.Errorf("unable to copy video into file: %w", err)
	}

	return nil
}

func (r *UpdateHandler) getTelegramFile(_ context.Context, fileID string) (io.ReadCloser, error) {
	file, err := r.bot.GetFile(tg.FileConfig{
		FileID: fileID,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to get file link: %w", err)
	}

	resp, err := http.Get(file.Link(r.bot.Token))
	if err != nil {
		return nil, fmt.Errorf("unable to download file: %w", err)
	}

	return resp.Body, nil
}

func (r *UpdateHandler) deleteMessages(ctx context.Context, chatID int64, messageIDs []int) error {
	var errs []error

	for _, messageID := range messageIDs {
		err := r.deleteMessage(ctx, chatID, messageID)
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) != 0 {
		return multierr.Combine(errs...)
	}

	return nil
}

func (r *UpdateHandler) deleteMessage(_ context.Context, chatID int64, messageID int) error {
	params := tg.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(messageID),
	}

	resp, err := r.bot.MakeRequest("deleteMessage", params)
	if err != nil {
		return fmt.Errorf("unable to make deleteMessage request: %w", err)
	}

	if !resp.Ok {
		return fmt.Errorf("error making deleteMessage reqeust: error_code: %d", resp.ErrorCode)
	}

	return nil
}

func (r *UpdateHandler) sendMediaGroup(ctx context.Context,
	chatID int64,
	files []any,
) ([]tg.Message, error) {
	msgs, err := r.bot.SendMediaGroup(tg.NewMediaGroup(chatID, files))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &ErrNotFound{
				Err: fmt.Errorf("unable to send media group: %w", err),
			}
		}
		return nil, fmt.Errorf("unable to send media group: %w", err)
	}

	return msgs, nil
}

func (r *UpdateHandler) sendSimplePoll(ctx context.Context,
	chatID int64,
	question string,
	answers []string,
) (*tg.Message, error) {
	opts := make([]tg.InputPollOption, 0, len(answers))
	for _, a := range answers {
		opts = append(opts, tg.NewPollOption(a))
	}

	poll := tg.NewPoll(chatID, question, opts...)
	poll.AllowsMultipleAnswers = true
	poll.IsAnonymous = false

	msg, err := r.bot.Send(poll)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &ErrNotFound{
				Err: fmt.Errorf("unable to send poll: %w", err),
			}
		}
		return nil, fmt.Errorf("unable to send poll: %w", err)
	}

	return &msg, nil
}

func (r *UpdateHandler) pinMessage(ctx context.Context,
	chatID int64,
	messageID int,
) error {
	_, err := r.bot.Send(tg.NewPinChatMessage(chatID, messageID, true))
	if err != nil {
		if strings.Contains(err.Error(), "cannot unmarshal bool into Go value of type tgbotapi.Message") {
			// sometimes tg api send back crap
			return nil
		}
		if strings.Contains(err.Error(), "not found") {
			return &ErrNotFound{
				Err: fmt.Errorf("unable to pin message: %w", err),
			}
		}
		return fmt.Errorf("unable to pin message: %w", err)
	}

	return nil
}
