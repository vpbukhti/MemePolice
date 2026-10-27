package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math/bits"
	"mime"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/NinaLeven/MemePolice/fsutils"
	"github.com/NinaLeven/MemePolice/videohash"
	tg "github.com/OvyFlash/telegram-bot-api"
	"github.com/corona10/goimagehash"
)

type UpdateHandler struct {
	bot     *tg.BotAPI
	storage StorageManager
	assets  Assets
}

func NewUpdateHandler(
	bot *tg.BotAPI,
	storage StorageManager,
	assets Assets,
) *UpdateHandler {
	return &UpdateHandler{
		bot:     bot,
		storage: storage,
		assets:  assets,
	}
}

type textPart string

func (t *textPart) UnmarshalJSON(data []byte) error {
	var str string
	err := json.Unmarshal(data, &str)
	if err == nil {
		*t = textPart(str)
		return nil
	}

	type textItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

	var item textItem
	err = json.Unmarshal(data, &item)
	if err != nil {
		return fmt.Errorf("failed to unmarshal Text: %v: %w", string(data), err)
	}

	*t = textPart(item.Text)

	return nil
}

type text string

func (t *text) UnmarshalJSON(data []byte) error {
	var str string
	err := json.Unmarshal(data, &str)
	if err == nil {
		*t = text(str)
		return nil
	}

	type textItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

	var items []textPart
	err = json.Unmarshal(data, &items)
	if err != nil {
		return fmt.Errorf("failed to unmarshal Text: %v: %w", string(data), err)
	}

	var combinedText string
	for _, item := range items {
		combinedText += string(item) + " "
	}
	*t = text(combinedText)

	return nil
}

func (r *UpdateHandler) OneTimeMigration(ctx context.Context, dataDirectoryPath string, chatID int64) error {
	type message struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Timestamp string `json:"date_unixtime"`
		FromName  string `json:"from"`
		FromID    string `json:"from_id"`
		PhotoPath string `json:"photo"`
		MediaType string `json:"media_type"`
		FilePath  string `json:"file"`
		Text      text   `json:"text"`
	}
	type dump struct {
		Messages []*message `json:"messages"`
	}

	getMessages := func() ([]*message, error) {
		resultFile, err := os.Open(path.Join(dataDirectoryPath, "result.json"))
		if err != nil {
			return nil, fmt.Errorf("unable to open result file: %w", err)
		}
		defer resultFile.Close()

		var data dump

		err = json.NewDecoder(resultFile).Decode(&data)
		if err != nil {
			return nil, fmt.Errorf("unale to unmarshal result file: %w", err)
		}

		return data.Messages, nil
	}

	const fileTooBig = "(File exceeds maximum size. Change data exporting settings to download.)"

	getPhotoHash := func(pth string) (*uint64, error) {
		if pth == "" || pth == fileTooBig {
			return nil, nil
		}

		photo, err := os.Open(path.Join(dataDirectoryPath, pth))
		if err != nil {
			return nil, fmt.Errorf("unable to open photo: %w", err)
		}
		defer photo.Close()

		img, _, err := image.Decode(photo)
		if err != nil {
			return nil, fmt.Errorf("unable to decode image: %w", err)
		}

		imgHash, err := goimagehash.PerceptionHash(img)
		if err != nil {
			return nil, fmt.Errorf("unable to calculate image perception hash: %w", err)
		}

		return ptr(imgHash.GetHash()), nil
	}

	getVideoHash := func(mediaType, pth string) (*uint64, *uint64, error) {
		if pth == "" || mediaType != "video_file" || pth == fileTooBig {
			return nil, nil, nil
		}

		vHash, aHash, err := videohash.PerceptualHash(path.Join(dataDirectoryPath, pth))
		if err != nil {
			return nil, nil, fmt.Errorf("unable to get video perceptual hash: %w", err)
		}

		return &vHash, &aHash, nil
	}

	processMessage := func(ctx context.Context, storage Storage, msg *message) error {
		if msg == nil || msg.Type != "message" {
			return nil
		}

		imgHash, err := getPhotoHash(msg.PhotoPath)
		if err != nil {
			return fmt.Errorf("unable to photo hash: %w", err)
		}

		vvHash, vaHash, err := getVideoHash(msg.MediaType, msg.FilePath)
		if err != nil {
			slog.ErrorContext(ctx, "unable to get video hash", slog.String("err", err.Error()))
		}

		userId, err := strconv.ParseInt(strings.TrimPrefix(msg.FromID, "user"), 10, 64)
		if err != nil {
			slog.ErrorContext(ctx, "unable to pasrse userId", slog.String("err", err.Error()))
		}

		timestampInt64, err := strconv.ParseInt(msg.Timestamp, 10, 64)
		if err != nil {
			return fmt.Errorf("unable to pasrse timestamp string: %w", err)
		}

		timestamp := time.Unix(timestampInt64, 0)

		cerr := storage.UpsertMessage(ctx, Message{
			MessageID: int(msg.ID),
			ChatID:    chatID,
			Raw: tg.Message{
				Chat: tg.Chat{
					ID: chatID,
				},
				From: &tg.User{
					ID: userId,
				},
				Text: (string(msg.Text))[0:min(len(msg.Text), 4096)],
			},
			ImageHash:      imgHash,
			VideoVideoHash: vvHash,
			VideoAudioHash: vaHash,
			CreatedAt:      timestamp,
			UpdatedAt:      timestamp,
		})
		if cerr != nil {
			return fmt.Errorf("unable to upsert message: %w", err)
		}

		return nil
	}

	messages, err := getMessages()
	if err != nil {
		slog.InfoContext(ctx, err.Error())

		return err
	}

	slog.InfoContext(ctx, "got messages", slog.Int("len", len(messages)))

	err = r.storage.ExecWithTx(ctx, func(ctx context.Context, storage Storage) error {
		for i, msg := range messages {
			slog.InfoContext(ctx, "processing message", slog.Int("i", i), slog.Int64("message_id", (msg.ID)))
			err := processMessage(ctx, storage, msg)
			if err != nil {
				slog.ErrorContext(ctx, "unable to process message", slog.Any("msg", msg))
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, err.Error())
		return fmt.Errorf("unable to exec in tx: %w", err)
	}

	return nil
}

func (r *UpdateHandler) HandleUpdates(ctx context.Context) error {
	lastUpdateID, err := r.storage.GetLastUpdateID(ctx)
	if err != nil {
		return fmt.Errorf("unable to get last update id: %w", err)
	}

	u := tg.NewUpdate(lastUpdateID)
	u.Timeout = 120
	u.AllowedUpdates = []string{
		"message",
		"message_reaction",
	}

	updates := r.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case update, ok := <-updates:
			if !ok {
				slog.InfoContext(ctx, "updates chan closed")
				return nil
			}

			slog.InfoContext(ctx, "handling update", slog.Int("update_id", update.UpdateID))

			err := r.handleUpdate(ctx, update)
			if err != nil {
				slog.ErrorContext(ctx, "unable to handle update", slog.String("error", err.Error()))
			}
		}
	}
}

func (r *UpdateHandler) handleUpdate(ctx context.Context, update tg.Update) error {
	err := r.storage.ExecWithTx(ctx, func(ctx context.Context, storage Storage) error {
		switch {
		case update.Message != nil:
			err := r.handleMessage(ctx, storage, update.Message)
			if err != nil {
				return fmt.Errorf("unable to handle message: %w", err)
			}

		case update.MessageReaction != nil:
			err := r.handleMessageReaction(ctx, storage, update.MessageReaction)
			if err != nil {
				return fmt.Errorf("unable to handle message reaction: %w", err)
			}

		default:
			slog.WarnContext(ctx, "unknown update", slog.Any("update", update))
			// more actions coming
		}

		err := storage.SetLastUpdateID(ctx, update.UpdateID)
		if err != nil {
			return fmt.Errorf("unable to set last update id: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("unable to exec in tx: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleMessageReaction(ctx context.Context, storage Storage, messageReaction *tg.MessageReactionUpdated) error {
	err := storage.UpsertMessageReactions(ctx, MessageReactions{
		MessageID: messageReaction.MessageID,
		ChatID:    messageReaction.Chat.ID,
		UserID:    messageReaction.User.ID,
		Reactions: messageReaction.NewReaction,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("unable to save message reactions: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleMessage(ctx context.Context, storage Storage, message *tg.Message) error {
	err := r.handleCommand(ctx, storage, message)
	if err != nil {
		return fmt.Errorf("unable to handle command: %w", err)
	}

	err = r.handleSettingPromptReply(ctx, storage, message)
	if err != nil {
		return fmt.Errorf("unable to handle setting prompt reply: %w", err)
	}

	var imageHash, videoVideoHash, videoAudioHash *uint64

	if message.From.ID != r.bot.Self.ID {
		imageHash, err = r.handleNewPhoto(ctx, storage, message)
		if err != nil {
			slog.ErrorContext(ctx, "unable to handle new photo", slog.String("err", err.Error()))
		}

		videoVideoHash, videoAudioHash, err = r.handleNewVideo(ctx, storage, message)
		if err != nil {
			slog.ErrorContext(ctx, "unable to handle new video", slog.String("err", err.Error()))
		}
	}

	err = storage.UpsertMessage(ctx, Message{
		MessageID:      message.MessageID,
		ChatID:         message.Chat.ID,
		Raw:            *message,
		ImageHash:      imageHash,
		VideoVideoHash: videoVideoHash,
		VideoAudioHash: videoAudioHash,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	})
	if err != nil {
		return fmt.Errorf("unable to save message image hash: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleCommand(ctx context.Context, storage Storage, message *tg.Message) (err error) {
	if !r.bot.IsMessageToMe(*message) {
		return nil
	}

	switch message.Command() {
	case "why":
		err := r.handleWhyCommand(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle why command: %w", err)
		}

	case "why_not":
		err := r.handleWhyNotCommand(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle why not command: %w", err)
		}

	case "why_the_fuck":
		err := r.handleWhyTheFuckCommand(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle why the fuck command: %w", err)
		}

	case "amend":
		err := r.handleAmend(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle amend: %w", err)
		}

	case "topkek":
		err := r.handleCreateTopkek(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle create topkek: %w", err)
		}

	case "preview":
		err := r.handlePreview(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle preview: %w", err)
		}

	case "stopkek":
		err := r.handleFinishTopkek(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle finish topkek: %w", err)
		}

	case "yearly_topkek":
		err := r.handleCreateYearlyTopkek(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle create yearly topkek: %w", err)
		}

	case "yearly_preview":
		err := r.handleYearlyPreview(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle yearly preview: %w", err)
		}

	case "settings":
		err := r.handleChatSettings(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle chat settings: %w", err)
		}

	case "setminreacts":
		err := r.handleChatSettingsNumeric(ctx, storage, message, numericSettings["min_reactions"])
		if err != nil {
			return fmt.Errorf("unable to handle chat settings min reactions: %w", err)
		}

	case "setimghdist":
		err := r.handleChatSettingsNumeric(ctx, storage, message, numericSettings["image_hamming_distance"])
		if err != nil {
			return fmt.Errorf("unable to handle chat settings image hamming distance: %w", err)
		}

	case "setvidhdist":
		err := r.handleChatSettingsNumeric(ctx, storage, message, numericSettings["video_hamming_distance"])
		if err != nil {
			return fmt.Errorf("unable to handle chat settings video hamming distance: %w", err)
		}

	case "setrevlimit":
		err := r.handleChatSettingsNumeric(ctx, storage, message, numericSettings["reverse_lookup_limit"])
		if err != nil {
			return fmt.Errorf("unable to handle chat settings reverse lookup limit: %w", err)
		}

	case "autotopkek":
		err := r.handleChatSettingsAutoTopkek(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle chat settings autotopkek: %w", err)
		}

	case "graceoffset":
		err := r.handleChatSettingsGraceOffset(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle chat settings grace offset: %w", err)
		}

	case "help":
		err := r.handleHelp(ctx, storage, message)
		if err != nil {
			return fmt.Errorf("unable to handle help: %w", err)
		}

	default:
		slog.WarnContext(ctx, "unknown command", slog.String("command", message.Command()))
	}

	return nil
}

func (r *UpdateHandler) handleAmend(ctx context.Context, storage Storage, message *tg.Message) (err error) {
	repeatedMsg, err := r.findRepeatedMessage(ctx, storage, message)
	if err != nil {
		return fmt.Errorf("unable to find repeated message: %w", err)
	}
	if repeatedMsg == nil {
		return nil
	}

	err = r.unsendReaction(ctx, storage, message.Chat.ID, repeatedMsg.MessageID)
	if err != nil {
		return fmt.Errorf("unable to unsend message reaction: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleWhyCommand(ctx context.Context, storage Storage, message *tg.Message) (err error) {
	_, origMsg, err := r.findOriginalMessage(ctx, storage, message)
	if err != nil {
		return fmt.Errorf("unable to find original message: %w", err)
	}
	if origMsg == nil {
		return nil
	}

	_, err = r.sendOriginalMessageInfo(ctx, message.Chat.ID, message.MessageID, origMsg)
	if err != nil {
		return fmt.Errorf("unable to send original message info: %w", err)
	}

	return nil
}

const whyTheFuckMediaTimeout = time.Minute

func (r *UpdateHandler) handleWhyTheFuckCommand(ctx context.Context, storage Storage, message *tg.Message) error {
	repeatedMsg, origMsg, err := r.findOriginalMessage(ctx, storage, message)
	if err != nil {
		return fmt.Errorf("unable to find original message: %w", err)
	}
	if origMsg == nil {
		return nil
	}

	mediaMsg, err := r.sendMediaReply(ctx,
		message.Chat.ID,
		message.MessageID,
		&origMsg.Raw,
		formatOriginalMessageInfo(origMsg.Raw),
		originalMessageLinkText,
		messageLink(message.Chat.ID, origMsg.MessageID),
	)
	if err != nil && !errors.Is(err, &ErrNotFound{}) && !errors.Is(err, errNoMedia) {
		return fmt.Errorf("unable to send original media: %w", err)
	}
	if err != nil {
		slog.WarnContext(ctx, "error sending original media",
			slog.String("err", err.Error()),
			slog.Int("repeated_msg_id", repeatedMsg.MessageID),
			slog.Any("orig_msg", origMsg),
		)
		_, err = r.sendOriginalMessageInfo(ctx, message.Chat.ID, message.MessageID, origMsg)
		if err != nil {
			return fmt.Errorf("unable to send original message info: %w", err)
		}
		return nil
	}

	go func() {
		select {
		case <-time.After(whyTheFuckMediaTimeout):
		case <-ctx.Done():
		}
		err := r.deleteMessage(ctx, message.Chat.ID, mediaMsg.MessageID)
		if err != nil {
			slog.ErrorContext(ctx, "unable to delete original media", slog.String("err", err.Error()))
		}
	}()

	return nil
}

const whyNotTimeout = time.Minute

var whyNotHammingDistances = []int{2, 4, 8, 16, 32, 64}

func (r *UpdateHandler) handleWhyNotCommand(ctx context.Context, storage Storage, message *tg.Message) error {
	if message.ReplyToMessage == nil {
		err := r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_reply", r.assets.GetAudioNoRererence())
		if err != nil {
			return fmt.Errorf("unable to send no_reply voice message %w", err)
		}
		return nil
	}

	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	selectedMsg, err := storage.GetMessage(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get selected message: %w", err)
	}

	var hits []Message
	if selectedMsg != nil {
		hits, err = r.findClosestMessages(ctx, storage, selectedMsg, chatSettings.ReverseLookupLimit)
		if err != nil {
			return fmt.Errorf("unable to find closest messages: %w", err)
		}
	}

	if len(hits) == 0 {
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_repeat", r.assets.GetAudioNoRepeat())
		if err != nil {
			return fmt.Errorf("unable to send no_repeat voice message %w", err)
		}
		return nil
	}

	for _, hit := range hits {
		err := r.sendWhyNotHit(ctx, message, selectedMsg, &hit)
		if err != nil {
			return fmt.Errorf("unable to send why not hit: %w", err)
		}
	}

	return nil
}

func (r *UpdateHandler) findClosestMessages(ctx context.Context, storage Storage, msg *Message, limit int) ([]Message, error) {
	var res []Message

	for _, hdist := range whyNotHammingDistances {
		var err error

		switch {
		case msg.ImageHash != nil:
			res, err = storage.ListClosestMessagesByImageHash(ctx, msg.ChatID, *msg.ImageHash, hdist, msg.MessageID, limit)

		case msg.VideoVideoHash != nil && msg.VideoAudioHash != nil:
			res, err = storage.ListClosestMessagesByVideoHash(ctx, msg.ChatID, *msg.VideoVideoHash, *msg.VideoAudioHash, hdist, msg.MessageID, limit)

		default:
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("unable to list closest messages: %w", err)
		}

		if len(res) >= limit {
			break
		}
	}

	return res, nil
}

func (r *UpdateHandler) sendWhyNotHit(ctx context.Context, message *tg.Message, selectedMsg *Message, hit *Message) error {
	caption := formatWhyNotInfo(*selectedMsg, *hit)
	link := messageLink(message.Chat.ID, hit.MessageID)

	var replyID int

	mediaMsg, err := r.sendMediaReply(ctx, message.Chat.ID, message.MessageID, &hit.Raw, caption, originalMessageLinkText, link)
	if err != nil && !errors.Is(err, &ErrNotFound{}) && !errors.Is(err, errNoMedia) {
		return fmt.Errorf("unable to send hit media: %w", err)
	}
	if err != nil {
		slog.WarnContext(ctx, "error sending hit media",
			slog.String("err", err.Error()),
			slog.Any("hit", hit),
		)
		replyID, err = r.sendLinkReply(ctx, message.Chat.ID, message.MessageID, caption, originalMessageLinkText, link)
		if err != nil {
			return fmt.Errorf("unable to send hit info: %w", err)
		}
	} else {
		replyID = mediaMsg.MessageID
	}

	go func() {
		select {
		case <-time.After(whyNotTimeout):
		case <-ctx.Done():
		}
		err := r.deleteMessage(ctx, message.Chat.ID, replyID)
		if err != nil {
			slog.ErrorContext(ctx, "unable to delete why not hit", slog.String("err", err.Error()))
		}
	}()

	return nil
}

func formatWhyNotInfo(selectedMsg Message, hit Message) string {
	lines := []string{formatOriginalMessageInfo(hit.Raw)}

	switch {
	case hit.ImageHash != nil && selectedMsg.ImageHash != nil:
		lines = append(lines, formatHashComparison("Изображение", *hit.ImageHash, *selectedMsg.ImageHash))

	case hit.VideoVideoHash != nil && hit.VideoAudioHash != nil &&
		selectedMsg.VideoVideoHash != nil && selectedMsg.VideoAudioHash != nil:
		lines = append(lines,
			formatHashComparison("Видео", *hit.VideoVideoHash, *selectedMsg.VideoVideoHash),
			formatHashComparison("Аудио", *hit.VideoAudioHash, *selectedMsg.VideoAudioHash),
		)
	}

	return strings.Join(lines, "\n\n")
}

func formatHashComparison(label string, origHash, selectedHash uint64) string {
	likeness := 64 - bits.OnesCount64(origHash^selectedHash)

	return fmt.Sprintf("%s:\n%016x\n%016x\nСхожесть: %d/64 (%.0f%%)",
		label,
		origHash,
		selectedHash,
		likeness,
		float64(likeness)*100/64,
	)
}

func formatOriginalMessageInfo(msg tg.Message) string {
	sentAt := time.Unix(msg.Date, 0).UTC().Format("02.01.2006 15:04 UTC")

	if msg.From == nil {
		return sentAt
	}

	author := strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	if author == "" {
		author = msg.From.UserName
	}
	if author == "" {
		return sentAt
	}

	return fmt.Sprintf("%s %s", author, sentAt)
}

const originalMessageLinkText = "Оригинал"

func messageLink(chatID int64, messageID int) string {
	const supergroupChatIDPrefix = -1000000000000

	if chatID > supergroupChatIDPrefix {
		return ""
	}

	return fmt.Sprintf("https://t.me/c/%d/%d", supergroupChatIDPrefix-chatID, messageID)
}

func (r *UpdateHandler) sendOriginalMessageInfo(ctx context.Context, chatID int64, replyToMessageID int, origMsg *Message) (int, error) {
	return r.sendLinkReply(ctx,
		chatID,
		replyToMessageID,
		formatOriginalMessageInfo(origMsg.Raw),
		originalMessageLinkText,
		messageLink(chatID, origMsg.MessageID),
	)
}

func (r *UpdateHandler) findRepeatedMessage(ctx context.Context, storage Storage, message *tg.Message) (*Message, error) {
	var repeatedMsg *Message
	var err error

	if message.ReplyToMessage != nil {
		repeatedMsg, err = storage.GetMessage(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	} else {
		repeatedMsg, err = storage.GetLastMessageWithUserReaction(ctx, message.Chat.ID, r.bot.Self.ID, RepeatedMemeEmoji)
	}
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return nil, fmt.Errorf("unable to get repeated message: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) && message.ReplyToMessage == nil {
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_reply", r.assets.GetAudioNoRererence())
		if err != nil {
			return nil, fmt.Errorf("unable to send no_reply voice message %w", err)
		}
		return nil, nil
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) {
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_repeat", r.assets.GetAudioNoRepeat())
		if err != nil {
			return nil, fmt.Errorf("unable to send no_repeat voice message %w", err)
		}
		return nil, nil
	}

	return repeatedMsg, nil
}

func (r *UpdateHandler) findOriginalMessage(ctx context.Context, storage Storage, message *tg.Message) (*Message, *Message, error) {
	repeatedMsg, err := r.findRepeatedMessage(ctx, storage, message)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to find repeated message: %w", err)
	}
	if repeatedMsg == nil {
		return nil, nil, nil
	}

	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	var origMsg *Message

	switch {
	case repeatedMsg.ImageHash != nil:
		origMsg, err = storage.GetFirstMatchingMessageByImageHash(ctx, message.Chat.ID, *repeatedMsg.ImageHash, chatSettings.ImageHammingDistance)

	case repeatedMsg.VideoVideoHash != nil && repeatedMsg.VideoAudioHash != nil:
		origMsg, err = storage.GetFirstMatchingMessageByVideoHash(ctx, message.Chat.ID, *repeatedMsg.VideoVideoHash, *repeatedMsg.VideoAudioHash, chatSettings.VideoHammingDistance)

	default:
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_repeat", r.assets.GetAudioNoRepeat())
		if err != nil {
			return nil, nil, fmt.Errorf("unable to send no_repeat voice message %w", err)
		}
		return nil, nil, nil
	}
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return nil, nil, fmt.Errorf("unable to get first matching message image hash: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) {
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "message_deleted", r.assets.GetAudioMessageDeleted())
		if err != nil {
			return nil, nil, fmt.Errorf("unable to send message_deleted voice message %w", err)
		}
		return nil, nil, nil
	}
	if origMsg.MessageID == repeatedMsg.MessageID {
		err = r.sendVoiceMessageReply(ctx, message.Chat.ID, message.MessageID, "no_repeat", r.assets.GetAudioNoRepeat())
		if err != nil {
			return nil, nil, fmt.Errorf("unable to send no_repeat voice message %w", err)
		}
		return nil, nil, nil
	}

	return repeatedMsg, origMsg, nil
}

const deleteAutoReplyTimeout = time.Hour

func (r *UpdateHandler) handleNewVideo(ctx context.Context, storage Storage, message *tg.Message) (*uint64, *uint64, error) {
	if message.Video == nil {
		return nil, nil, nil
	}
	if message.Video.FileSize > 1024*1024*120 {
		slog.WarnContext(ctx, "video too big", slog.Int64("size", message.Video.FileSize))
		return nil, nil, nil
	}

	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	tempDir, err := fsutils.GetTempDir()
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get temp dir: %w", err)
	}
	defer fsutils.CleanupTempDir(tempDir)

	ext, err := mime.ExtensionsByType(message.Video.MimeType)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to determine mime type: %s: %w", message.Video.MimeType, err)
	}
	if len(ext) == 0 {
		return nil, nil, fmt.Errorf("unknown mime type: %s", message.Video.MimeType)
	}

	tempVideoPath := path.Join(tempDir, "video"+ext[0])

	err = r.getTelegramVideo(ctx, message.Video.FileID, tempVideoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get telegram video: %w", err)
	}

	videoHash, audioHash, err := videohash.PerceptualHash(tempVideoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to calculate video perception hash: %w", err)
	}

	origMessage, err := storage.GetFirstMatchingMessageByVideoHash(ctx, message.Chat.ID, videoHash, audioHash, chatSettings.VideoHammingDistance)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return &videoHash, &audioHash, fmt.Errorf("unable to get lash matching message video hash: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) || origMessage.MessageID == message.MessageID {
		return &videoHash, &audioHash, nil
	}

	err = r.sendReaction(ctx, storage, message.Chat.ID, message.MessageID, RepeatedMemeEmoji)
	if err != nil {
		return &videoHash, &audioHash, fmt.Errorf("unable to send stale meme reaction: %w", err)
	}

	replyID, err := r.sendOriginalMessageInfo(ctx, message.Chat.ID, message.MessageID, origMessage)
	if err != nil {
		return &videoHash, &audioHash, fmt.Errorf("unable to send stale meme reply: %w", err)
	}

	go func() {
		select {
		case <-time.After(deleteAutoReplyTimeout):
		case <-ctx.Done():
		}
		err := r.deleteMessage(ctx, message.Chat.ID, replyID)
		if err != nil {
			slog.ErrorContext(ctx, "unable to delete reply", slog.String("err", err.Error()))
		}
	}()

	return &videoHash, &audioHash, nil
}

func (r *UpdateHandler) handleNewPhoto(ctx context.Context, storage Storage, message *tg.Message) (*uint64, error) {
	if len(message.Photo) == 0 {
		return nil, nil
	}

	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return nil, fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	// photo with max resolution
	photo := message.Photo[len(message.Photo)-1]

	img, err := r.getTelegramImage(ctx, photo.FileID)
	if err != nil {
		return nil, fmt.Errorf("unable to get telegram photo: %w", err)
	}

	imgHash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return nil, fmt.Errorf("unable to calculate image perception hash: %w", err)
	}

	origMessage, err := storage.GetFirstMatchingMessageByImageHash(ctx, message.Chat.ID, imgHash.GetHash(), chatSettings.ImageHammingDistance)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return ptr(imgHash.GetHash()), fmt.Errorf("unable to get lash matching message image hash: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) || origMessage.MessageID == message.MessageID {
		return ptr(imgHash.GetHash()), nil
	}

	err = r.sendReaction(ctx, storage, message.Chat.ID, message.MessageID, RepeatedMemeEmoji)
	if err != nil {
		return ptr(imgHash.GetHash()), fmt.Errorf("unable to send stale meme reaction: %w", err)
	}

	replyID, err := r.sendOriginalMessageInfo(ctx, message.Chat.ID, message.MessageID, origMessage)
	if err != nil {
		return ptr(imgHash.GetHash()), fmt.Errorf("unable to send stale meme reply: %w", err)
	}

	go func() {
		select {
		case <-time.After(deleteAutoReplyTimeout):
		case <-ctx.Done():
		}
		err := r.deleteMessage(ctx, message.Chat.ID, replyID)
		if err != nil {
			slog.ErrorContext(ctx, "unable to delete reply", slog.String("err", err.Error()))
		}
	}()

	return ptr(imgHash.GetHash()), nil
}

func (r *UpdateHandler) getOrCreateChatSettings(ctx context.Context, storage Storage, chatID int64) (*ChatSettings, error) {
	chatSettings, err := storage.GetChatSettings(ctx, chatID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return nil, fmt.Errorf("unable to get chat settings: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) {
		chatSettings := defaultChatSettings(chatID)

		err := r.storage.UpsertChatSettings(ctx, chatSettings)
		if err != nil {
			return nil, fmt.Errorf("unable to upsert chat settings: %w", err)
		}

		return &chatSettings, nil
	}

	return chatSettings, nil
}

func formatChatSettings(settings ChatSettings) string {
	return fmt.Sprintf(`Настройки чата:
* Автотопкек: %v
* Льготный период: %v
* Порог реакций: %d
* Порог схожести изображений: %d
* Порог схожести видео: %d
* Лимит обратного поиска: %d`,
		settings.IsAutoTopkek,
		settings.IsGraceOffset,
		settings.MinReactions,
		settings.ImageHammingDistance,
		settings.VideoHammingDistance,
		settings.ReverseLookupLimit,
	)
}

func (r *UpdateHandler) handleChatSettings(ctx context.Context, storage Storage, message *tg.Message) error {
	err := r.sendOutChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to send out chat settings: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendOutChatSettings(ctx context.Context, storage Storage, chatID int64) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, chatID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	_, err = r.sendMessage(ctx, chatID, formatChatSettings(*chatSettings))
	if err != nil {
		return fmt.Errorf("unable to send message: %w", err)
	}

	return nil
}

type numericSetting struct {
	Name  string
	Label string
	Min   int
	Max   int
	Get   func(settings ChatSettings) int
	Set   func(settings *ChatSettings, value int)
}

var numericSettings = map[string]numericSetting{
	"min_reactions": {
		Name:  "min_reactions",
		Label: "порог реакций",
		Min:   0,
		Max:   100,
		Get:   func(settings ChatSettings) int { return settings.MinReactions },
		Set:   func(settings *ChatSettings, value int) { settings.MinReactions = value },
	},
	"image_hamming_distance": {
		Name:  "image_hamming_distance",
		Label: "порог схожести изображений",
		Min:   0,
		Max:   64,
		Get:   func(settings ChatSettings) int { return settings.ImageHammingDistance },
		Set:   func(settings *ChatSettings, value int) { settings.ImageHammingDistance = value },
	},
	"video_hamming_distance": {
		Name:  "video_hamming_distance",
		Label: "порог схожести видео",
		Min:   0,
		Max:   64,
		Get:   func(settings ChatSettings) int { return settings.VideoHammingDistance },
		Set:   func(settings *ChatSettings, value int) { settings.VideoHammingDistance = value },
	},
	"reverse_lookup_limit": {
		Name:  "reverse_lookup_limit",
		Label: "лимит обратного поиска",
		Min:   1,
		Max:   10,
		Get:   func(settings ChatSettings) int { return settings.ReverseLookupLimit },
		Set:   func(settings *ChatSettings, value int) { settings.ReverseLookupLimit = value },
	},
}

func (r *UpdateHandler) handleChatSettingsNumeric(ctx context.Context, storage Storage, message *tg.Message, setting numericSetting) error {
	if strings.TrimSpace(message.CommandArguments()) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(message.CommandArguments()))
		if err != nil {
			_, err = r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "аргумент должен быть числом")
			if err != nil {
				return fmt.Errorf("unable to send int parse error reply: %w", err)
			}
			return nil
		}

		err = r.setNumericSetting(ctx, storage, message.Chat.ID, setting, value)
		if err != nil {
			return fmt.Errorf("unable to set numeric setting: %w", err)
		}
		return nil
	}

	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	promptMsg, err := r.sendForceReply(ctx,
		message.Chat.ID,
		message.MessageID,
		fmt.Sprintf("Введи %s", setting.Label),
		fmt.Sprintf("Сейчас: %d (%d–%d)", setting.Get(*chatSettings), setting.Min, setting.Max),
	)
	if err != nil {
		return fmt.Errorf("unable to send setting prompt: %w", err)
	}

	err = storage.CreateSettingPrompt(ctx, SettingPrompt{
		ChatID:    message.Chat.ID,
		MessageID: promptMsg.MessageID,
		UserID:    message.From.ID,
		Setting:   setting.Name,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to create setting prompt: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleSettingPromptReply(ctx context.Context, storage Storage, message *tg.Message) error {
	if message.ReplyToMessage == nil ||
		message.ReplyToMessage.From == nil ||
		message.ReplyToMessage.From.ID != r.bot.Self.ID ||
		message.IsCommand() {
		return nil
	}

	prompt, err := storage.GetSettingPrompt(ctx, message.Chat.ID, message.ReplyToMessage.MessageID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get setting prompt: %w", err)
	}
	if err != nil && errors.Is(err, &ErrNotFound{}) || prompt.UserID != message.From.ID {
		return nil
	}

	setting, ok := numericSettings[prompt.Setting]
	if !ok {
		return fmt.Errorf("unknown setting: %s", prompt.Setting)
	}

	value, err := strconv.Atoi(strings.TrimSpace(message.Text))
	if err != nil {
		_, err = r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "аргумент должен быть числом")
		if err != nil {
			return fmt.Errorf("unable to send int parse error reply: %w", err)
		}
		return nil
	}

	err = storage.DeleteSettingPrompt(ctx, prompt.ChatID, prompt.MessageID)
	if err != nil {
		return fmt.Errorf("unable to delete setting prompt: %w", err)
	}

	err = r.setNumericSetting(ctx, storage, message.Chat.ID, setting, value)
	if err != nil {
		return fmt.Errorf("unable to set numeric setting: %w", err)
	}

	return nil
}

func (r *UpdateHandler) setNumericSetting(ctx context.Context, storage Storage, chatID int64, setting numericSetting, value int) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, chatID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	setting.Set(chatSettings, min(max(setting.Min, value), setting.Max))

	err = storage.UpsertChatSettings(ctx, *chatSettings)
	if err != nil {
		return fmt.Errorf("unable to update chat settings: %w", err)
	}

	err = r.sendOutChatSettings(ctx, storage, chatID)
	if err != nil {
		return fmt.Errorf("unable to send out chat settings: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleChatSettingsAutoTopkek(ctx context.Context, storage Storage, message *tg.Message) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	chatSettings.IsAutoTopkek = !chatSettings.IsAutoTopkek

	err = r.storage.UpsertChatSettings(ctx, *chatSettings)
	if err != nil {
		return fmt.Errorf("unable to update chat settings: %w", err)
	}

	err = r.sendOutChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to send out chat settings: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleChatSettingsGraceOffset(ctx context.Context, storage Storage, message *tg.Message) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	chatSettings.IsGraceOffset = !chatSettings.IsGraceOffset

	err = r.storage.UpsertChatSettings(ctx, *chatSettings)
	if err != nil {
		return fmt.Errorf("unable to update chat settings: %w", err)
	}

	err = r.sendOutChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to send out chat settings: %w", err)
	}

	return nil
}
