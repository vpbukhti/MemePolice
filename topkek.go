package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	tg "github.com/OvyFlash/telegram-bot-api"
)

func defaultTopkekName() string {
	return fmt.Sprintf("Топкек %02d.%02d.%04d",
		time.Now().UTC().Day(),
		time.Now().UTC().Month(),
		time.Now().UTC().Year(),
	)
}

func parseCreateTopkekOptions(chatSettings ChatSettings, message *tg.Message) createTopkekOptions {
	opts := createTopkekOptions{
		ChatID:       message.Chat.ID,
		AuthorID:     message.From.ID,
		MessageID:    message.MessageID,
		Name:         defaultTopkekName(),
		MinReactions: chatSettings.MinReactions,
	}

	if message.ReplyToMessage != nil {
		opts.StartingMessageID = &message.ReplyToMessage.MessageID
	}

	if message.CommandArguments() != "" {
		opts.Name = message.CommandArguments()
	}

	return opts
}

func (r *UpdateHandler) handleCreateTopkek(ctx context.Context, storage Storage, message *tg.Message) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	opts := parseCreateTopkekOptions(*chatSettings, message)

	err = r.createTopkek(ctx, storage, opts)
	if err != nil &&
		!errors.Is(err, errNotEnoughTopkekSrcs) &&
		!errors.Is(err, errNoTopkekStartMessage) &&
		!errors.Is(err, errTopkekAlreadyInProgress) {
		return fmt.Errorf("unable to create topkek: %w", err)
	}
	if err != nil && errors.Is(err, errNotEnoughTopkekSrcs) {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "надо хотя бы два мема")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNotEnoughTopkekSrcs
	}
	if err != nil && errors.Is(err, errTopkekAlreadyInProgress) {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "топкек уже идет")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errTopkekAlreadyInProgress
	}
	if err != nil && errors.Is(err, errNoTopkekStartMessage) {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "надо реплай с какого сообщения начать топкек")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNoTopkekStartMessage
	}

	return nil
}

var (
	errTopkekAlreadyInProgress = errors.New("not enough topkek already in progress")
	errNoTopkekStartMessage    = errors.New("no topkek starting message")
)

type createTopkekOptions struct {
	MessageID         int
	ChatID            int64
	Name              string
	AuthorID          int64
	StartingMessageID *int
	MinReactions      int
}

func (r *UpdateHandler) createTopkek(ctx context.Context, storage Storage, opts createTopkekOptions) error {
	lastTopkek, err := storage.GetLastTopkek(ctx, opts.ChatID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get latest topkek: %w", err)
	}

	if lastTopkek != nil && lastTopkek.Status != TopkekStatusDone {
		return errTopkekAlreadyInProgress
	}
	if lastTopkek == nil && opts.StartingMessageID == nil {
		return errNoTopkekStartMessage
	}

	listOpts := ListMessagesWithReactionCountOptions{
		ChatID:           opts.ChatID,
		MinReactions:     opts.MinReactions,
		ExcludeReactions: ExcludeReactions,
	}
	if lastTopkek != nil {
		listOpts.StartingMessageID = lastTopkek.MessageID
	}
	if opts.StartingMessageID != nil {
		listOpts.StartingMessageID = *opts.StartingMessageID
	}

	sourceMessages, err := storage.ListMessagesWithReactionCount(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("unable to find topkek source messages: %w", err)
	}

	if len(sourceMessages) < 2 {
		return errNotEnoughTopkekSrcs
	}

	topkekID, err := storage.CreateTopkek(ctx, Topkek{
		Name:      opts.Name,
		AuthorID:  opts.AuthorID,
		ChatID:    opts.ChatID,
		MessageID: opts.MessageID,
		Status:    TopkekStatusStarted,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to create topkek: %w", err)
	}

	topkek, err := storage.GetTopkek(ctx, topkekID)
	if err != nil {
		return fmt.Errorf("unable to get current topkek: %w", err)
	}

	err = r.startTopkek(ctx, storage, topkek, messagesToTG(sourceMessages))
	if err != nil {
		return fmt.Errorf("unable to start topkek: %w", err)
	}

	return nil
}

func messagesToTG(r []Message) []*tg.Message {
	res := make([]*tg.Message, 0, len(r))
	for _, msg := range r {
		res = append(res, &msg.Raw)
	}
	return res
}

func (r *UpdateHandler) getTopkekMessages(ctx context.Context, storage Storage, topkekID int64, msgType TopkekMessageType) ([]TopkekMessage, error) {
	topkekMessages, err := storage.GetTopkekMessages(ctx, topkekID)
	if err != nil {
		return nil, fmt.Errorf("unable to get topkek messages: %w", err)
	}

	res := []TopkekMessage{}

	for _, msg := range topkekMessages {
		if msg.Type != msgType {
			continue
		}

		res = append(res, msg)
	}

	return res, nil
}

func maxChunkSize(n int) int {
	if n <= 10 {
		return n
	}
	c := 10
	for i := 10; i > 5; i-- {
		if i-n%i < c-n%c {
			c = i
		}
		if n%i == 0 {
			return i
		}
	}
	return c
}

func chunkMessages(srcs []*tg.Message) [][]*tg.Message {
	chunkSize := maxChunkSize(len(srcs))

	res := [][]*tg.Message{}
	for i := 0; i < len(srcs); i += chunkSize {
		end := i + chunkSize
		if end > len(srcs) {
			end = len(srcs)
		}
		res = append(res, srcs[i:end])
	}

	return res
}

var errNotEnoughTopkekSrcs = errors.New("not enough topkek srcs")

func (r *UpdateHandler) startTopkek(ctx context.Context, storage Storage, topkek *Topkek, srcs []*tg.Message) error {
	if topkek.Status != TopkekStatusStarted {
		return fmt.Errorf("invalid topkek status: %s", topkek.Status)
	}

	if len(srcs) < 2 {
		return errNotEnoughTopkekSrcs
	}

	chunks := chunkMessages(srcs)
	i := 0
	for _, chunk := range chunks {
		err := r.sendTopkekChunk(ctx, storage, topkek, chunk, i)
		if err != nil {
			return fmt.Errorf("unable to send topkek chunk: %w", err)
		}
		i += len(chunk)
	}

	err := storage.UpdateTopkekStatus(ctx, topkek.ID, TopkekStatusStarted)
	if err != nil {
		return fmt.Errorf("unable to update topkek: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendTopkekChunk(ctx context.Context, storage Storage, topkek *Topkek, srcs []*tg.Message, srcOffset int) error {
	for _, msg := range srcs {
		err := storage.CreateTopkekMessage(ctx, TopkekMessage{
			TopkekID:        topkek.ID,
			ChatID:          topkek.ChatID,
			MessageID:       msg.MessageID,
			SourceMessageID: msg.MessageID,
			Type:            TopkekMessageTypeSrc,
			Raw:             *msg,
			CreatedAt:       time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("unable to create topkek src message: %w", err)
		}
	}

	pollAnswers := []string{}
	files := []any{}

	for i, msg := range srcs {
		switch {
		case len(msg.Photo) > 0:
			photo := msg.Photo[len(msg.Photo)-1]
			files = append(files, tg.NewInputMediaPhoto(tg.FileID(photo.FileID)))

		case msg.Video != nil:
			files = append(files, tg.NewInputMediaVideo(tg.FileID(msg.Video.FileID)))

		default:
			continue
		}

		pollAnswers = append(pollAnswers, strconv.Itoa(srcOffset+i+1))
	}

	msgIds, err := r.sendMediaGroup(ctx, topkek.ChatID, files)
	if err != nil {
		return fmt.Errorf("unable to send media group: %w", err)
	}

	if len(srcs) != len(msgIds) {
		return fmt.Errorf("not all topkek candidates msgs are sent: expected %d, got %d", len(srcs), len(msgIds))
	}

	for i, msg := range msgIds {
		err := storage.CreateTopkekMessage(ctx, TopkekMessage{
			TopkekID:        topkek.ID,
			ChatID:          topkek.ChatID,
			MessageID:       msg.MessageID,
			SourceMessageID: srcs[i].MessageID,
			Type:            TopkekMessageTypeDst,
			Raw:             msg,
			CreatedAt:       time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("unable to create topkek dst message: %w", err)
		}
	}

	pollRes, err := r.sendSimplePoll(ctx, topkek.ChatID, topkek.Name, pollAnswers)
	if err != nil {
		return fmt.Errorf("unable to send topkek poll: %w", err)
	}

	if srcOffset == 0 {
		err := r.pinMessage(ctx, topkek.ChatID, pollRes.MessageID)
		if err != nil {
			slog.ErrorContext(ctx, "unable to pin first topkek poll", slog.String("err", err.Error()))
		}
	}

	err = storage.CreateTopkekMessage(ctx, TopkekMessage{
		TopkekID:  topkek.ID,
		ChatID:    topkek.ChatID,
		MessageID: pollRes.MessageID,
		Type:      TopkekMessageTypePoll,
		Raw:       *pollRes,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to create topkek dst message: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleFinishTopkek(ctx context.Context, storage Storage, message *tg.Message) error {
	topkek, err := storage.GetLastTopkek(ctx, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get latest topkek: %w", err)
	}

	if message.CommandArguments() == "-f" {
		err := r.finishTopkekForce(ctx, storage, topkek.ID)
		if err != nil {
			return fmt.Errorf("unbale to force finish topkek: %w", err)
		}
		return nil
	}

	if topkek.Status != TopkekStatusStarted {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "сначала начни топкек, шкура")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return nil
	}

	err = r.finishTopkek(ctx, storage, topkek.ID)
	if err != nil {
		return fmt.Errorf("unable to finish topkek: %w", err)
	}

	return nil
}

func getPollWinners(opts []tg.PollOption) []int {
	m := 0
	for _, opt := range opts {
		if opt.VoterCount > m {
			m = opt.VoterCount
		}
	}

	res := []int{}
	for _, opt := range opts {
		num, err := strconv.Atoi(opt.Text)
		if err != nil {
			slog.Error("unable to parse poll result text", slog.String("err", err.Error()))
		}
		if opt.VoterCount != m {
			continue
		}
		res = append(res, num-1)
	}

	return res
}

func (r *UpdateHandler) finishTopkekForce(ctx context.Context, storage Storage, topkekID int64) error {
	err := storage.UpdateTopkekStatus(ctx, topkekID, TopkekStatusDone)
	if err != nil {
		return fmt.Errorf("unable to update topkek: %w", err)
	}

	return nil
}

func (r *UpdateHandler) finishTopkek(ctx context.Context, storage Storage, topkekID int64) error {
	topkek, err := storage.GetTopkek(ctx, topkekID)
	if err != nil {
		return fmt.Errorf("unable to get topkek: %w", err)
	}

	pollIds, err := r.getTopkekMessages(ctx, storage, topkekID, TopkekMessageTypePoll)
	if err != nil {
		return fmt.Errorf("unable to get topkek polls: %w", err)
	}

	destMsgs, err := r.getTopkekMessages(ctx, storage, topkekID, TopkekMessageTypeDst)
	if err != nil {
		return fmt.Errorf("unable to get topkek descs: %w", err)
	}

	pollResutls := []tg.PollOption{}

	for _, pollMsg := range pollIds {
		poll, err := r.bot.StopPoll(tg.NewStopPoll(topkek.ChatID, pollMsg.MessageID))
		if err != nil {
			return fmt.Errorf("unable to stop poll: %w", err)
		}
		pollResutls = append(pollResutls, poll.Options...)
	}

	winners := getPollWinners(pollResutls)
	if len(winners) == 0 {
		return fmt.Errorf("0 topkek winners: %w", err)
	}

	if len(winners) > 1 {
		winnerMsgs := []*tg.Message{}
		for _, winner := range winners {
			winnerMsgs = append(winnerMsgs, &destMsgs[winner].Raw)
		}

		err := r.restartTopkek(ctx, storage, topkek, winnerMsgs)
		if err != nil {
			return fmt.Errorf("unable to restart topkek: %w", err)
		}

		return nil
	}

	winner := winners[0]

	winnerMsg := destMsgs[winner]
	var winnerMsgRes *tg.Message

	switch {
	case len(winnerMsg.Raw.Photo) > 0:
		photo := winnerMsg.Raw.Photo[len(winnerMsg.Raw.Photo)-1]
		winnerMsgRes, err = r.sendPhotoRepy(ctx,
			topkek.ChatID,
			winnerMsg.SourceMessageID,
			fmt.Sprintf("Победитель %s", topkek.Name),
			photo.FileID,
		)
		if err != nil {
			return fmt.Errorf("unable to send winner photo: %w", err)
		}

	case winnerMsg.Raw.Video != nil:
		winnerMsgRes, err = r.sendVideoRepy(ctx,
			topkek.ChatID,
			winnerMsg.SourceMessageID,
			fmt.Sprintf("Победитель %s", topkek.Name),
			winnerMsg.Raw.Video.FileID,
		)
		if err != nil {
			return fmt.Errorf("unable to send winner video: %w", err)
		}
	}

	err = r.pinMessage(ctx, topkek.ChatID, winnerMsgRes.MessageID)
	if err != nil {
		slog.ErrorContext(ctx, "unable to pin topkek winner", slog.String("err", err.Error()))
	}

	err = storage.CreateTopkekMessage(ctx, TopkekMessage{
		TopkekID:        topkekID,
		ChatID:          topkek.ChatID,
		MessageID:       winnerMsgRes.MessageID,
		SourceMessageID: winnerMsg.SourceMessageID,
		Type:            TopkekMessageTypeWinner,
		Raw:             *winnerMsgRes,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to cerate topkek winner msg: %w", err)
	}

	err = storage.UpdateTopkekStatus(ctx, topkekID, TopkekStatusDone)
	if err != nil {
		return fmt.Errorf("unable to update topkek: %w", err)
	}

	return nil
}

func (r *UpdateHandler) restartTopkek(ctx context.Context, storage Storage, topkek *Topkek, srcs []*tg.Message) error {
	err := storage.DeleteTopkekMessages(ctx, topkek.ID)
	if err != nil {
		return fmt.Errorf("unable to delete topkek old topkek msgs: %w", err)
	}

	err = storage.UpdateTopkekStatus(ctx, topkek.ID, TopkekStatusStarted)
	if err != nil {
		return fmt.Errorf("unable to update topkek: %w", err)
	}

	return r.startTopkek(ctx, storage, topkek, srcs)
}

func (r *UpdateHandler) handleHelp(ctx context.Context, _ Storage, message *tg.Message) error {
	const helpText = `Топкек инструкция:
* Создай топкек - /topkek
* По умолчанию топкек создается начиная с предыдушего
* Вместе с командой /topkek можно передать реплай на сообщение с которого должен начаться топкек
* Ждем сколько надо голосования
* Завершаем топкек /stopkek`

	_, err := r.sendMessage(ctx, message.Chat.ID, helpText)
	if err != nil {
		return fmt.Errorf("unable to send text message: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handlePreview(ctx context.Context, storage Storage, message *tg.Message) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	lastTopkek, err := storage.GetLastTopkek(ctx, message.Chat.ID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get latest topkek: %w", err)
	}

	listOpts := ListMessagesWithReactionCountOptions{
		ChatID:       message.Chat.ID,
		MinReactions: chatSettings.MinReactions,
		ExcludeReactions: [2]string{
			RepeatedMemeEmoji,
			StaleMemeEmoji,
		},
	}

	if lastTopkek == nil && message.ReplyToMessage == nil {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "надо реплай с какого сообщения начать топкек")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNoTopkekStartMessage
	}
	if lastTopkek != nil {
		listOpts.StartingMessageID = lastTopkek.MessageID
	}
	if message.ReplyToMessage != nil {
		listOpts.StartingMessageID = message.ReplyToMessage.MessageID
	}

	sourceMessages, err := storage.ListMessagesWithReactionCount(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("unable to find topkek source messages: %w", err)
	}

	if len(sourceMessages) == 0 {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "нет мемов в топкек")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNotEnoughTopkekSrcs
	}

	err = r.sendTemporaryMediaGroup(ctx, message.Chat.ID, messagesToTG(sourceMessages), time.Minute)
	if err != nil {
		return fmt.Errorf("unable to send out temporary media group: %w", err)
	}

	return nil
}

func (r *UpdateHandler) sendTemporaryMediaGroup(ctx context.Context, chatID int64, messages []*tg.Message, timeout time.Duration) error {
	chunks := chunkMessages(messages)
	for _, chunk := range chunks {
		files := []any{}

		for _, msg := range chunk {
			switch {
			case len(msg.Photo) > 0:
				photo := msg.Photo[len(msg.Photo)-1]
				files = append(files, tg.NewInputMediaPhoto(tg.FileID(photo.FileID)))

			case msg.Video != nil:
				files = append(files, tg.NewInputMediaVideo(tg.FileID(msg.Video.FileID)))

			default:
				continue
			}
		}

		sentMessages, err := r.sendMediaGroup(ctx, chatID, files)
		if err != nil {
			return fmt.Errorf("unable to send media group: %w", err)
		}

		msgIds := make([]int, 0, len(sentMessages))
		for _, msg := range sentMessages {
			msgIds = append(msgIds, msg.MessageID)
		}

		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(timeout):
				err := r.deleteMessages(ctx, chatID, msgIds)
				if err != nil {
					slog.ErrorContext(ctx, "unable to delete messages", "error", err, "msg_ids", msgIds)
				}
			}
		}()
	}

	return nil
}

func (r *UpdateHandler) handleCreateYearlyTopkek(ctx context.Context, storage Storage, message *tg.Message) error {
	chatSettings, err := r.getOrCreateChatSettings(ctx, storage, message.Chat.ID)
	if err != nil {
		return fmt.Errorf("unable to get or create chat settings: %w", err)
	}

	opts := parseCreateTopkekOptions(*chatSettings, message)
	opts.Name = fmt.Sprintf("Годовой Топкек %04d",
		time.Now().UTC().Year(),
	)
	opts.StartingMessageID = nil
	opts.MinReactions = 0

	err = r.createYearlyTopkek(ctx, storage, opts)
	if err != nil &&
		!errors.Is(err, errNotEnoughTopkekSrcs) &&
		!errors.Is(err, errNoTopkekStartMessage) &&
		!errors.Is(err, errTopkekAlreadyInProgress) {
		return fmt.Errorf("unable to create topkek: %w", err)
	}
	if err != nil && errors.Is(err, errNotEnoughTopkekSrcs) {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "надо хотя бы два мема")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNotEnoughTopkekSrcs
	}
	if err != nil && errors.Is(err, errTopkekAlreadyInProgress) {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "топкек уже идет")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errTopkekAlreadyInProgress
	}

	return nil
}

func topkekMessagesToTG(r []TopkekMessage) []*tg.Message {
	res := make([]*tg.Message, 0, len(r))
	for _, msg := range r {
		res = append(res, &msg.Raw)
	}
	return res
}

func (r *UpdateHandler) createYearlyTopkek(ctx context.Context, storage Storage, opts createTopkekOptions) error {
	lastTopkek, err := storage.GetLastTopkek(ctx, opts.ChatID)
	if err != nil && !errors.Is(err, &ErrNotFound{}) {
		return fmt.Errorf("unable to get latest topkek: %w", err)
	}

	if lastTopkek != nil && lastTopkek.Status != TopkekStatusDone {
		return errTopkekAlreadyInProgress
	}

	thisYear := time.Date(time.Now().UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	sourceMessages, err := storage.GetTopkekWinners(ctx, opts.ChatID, thisYear)
	if err != nil {
		return fmt.Errorf("unable to find topkek winners messages: %w", err)
	}

	if len(sourceMessages) < 2 {
		return errNotEnoughTopkekSrcs
	}

	topkekID, err := storage.CreateTopkek(ctx, Topkek{
		Name:      opts.Name,
		AuthorID:  opts.AuthorID,
		ChatID:    opts.ChatID,
		MessageID: opts.MessageID,
		Status:    TopkekStatusStarted,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("unable to create yearly topkek: %w", err)
	}

	topkek, err := storage.GetTopkek(ctx, topkekID)
	if err != nil {
		return fmt.Errorf("unable to get current topkek: %w", err)
	}

	err = r.startTopkek(ctx, storage, topkek, topkekMessagesToTG(sourceMessages))
	if err != nil {
		return fmt.Errorf("unable to start yearly topkek: %w", err)
	}

	return nil
}

func (r *UpdateHandler) handleYearlyPreview(ctx context.Context, storage Storage, message *tg.Message) error {
	thisYear := time.Date(time.Now().UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	sourceMessages, err := storage.GetTopkekWinners(ctx, message.Chat.ID, thisYear)
	if err != nil {
		return fmt.Errorf("unable to find topkek winners messages: %w", err)
	}

	if len(sourceMessages) == 0 {
		_, err := r.sendMessageReply(ctx, message.Chat.ID, message.MessageID, "нет мемов в годовой топкек")
		if err != nil {
			return fmt.Errorf("unable to send message reply: %w", err)
		}
		return errNotEnoughTopkekSrcs
	}

	err = r.sendTemporaryMediaGroup(ctx, message.Chat.ID, topkekMessagesToTG(sourceMessages), time.Minute)
	if err != nil {
		return fmt.Errorf("unable to send out temporary media group: %w", err)
	}

	return nil
}
