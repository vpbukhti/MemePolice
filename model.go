package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	tg "github.com/OvyFlash/telegram-bot-api"
)

func ptr[T any](t T) *T {
	return &t
}

func val[T any](t *T) T {
	if t == nil {
		var res T
		return res
	}
	return *t
}

type ErrNotFound struct {
	Err error
}

func (e *ErrNotFound) Error() string {
	if e.Err == nil {
		return "not found"
	}
	return fmt.Sprintf("not found: %s", e.Err.Error())
}

func (e *ErrNotFound) Is(target error) bool {
	_, ok := target.(*ErrNotFound)
	return ok
}

func (e *ErrNotFound) As(target interface{}) bool {
	if _, ok := target.(*ErrNotFound); ok {
		*target.(*ErrNotFound) = *e
		return true
	}
	return errors.As(e.Err, target)
}

const (
	StaleMemeEmoji    = "🥱"
	RepeatedMemeEmoji = "✍️"
	OKEmoji           = "👌"
)

var ExcludeReactions = [2]string{
	RepeatedMemeEmoji,
	StaleMemeEmoji,
}

type Message struct {
	MessageID      int
	ChatID         int64
	Raw            tg.Message
	ImageHash      *uint64
	VideoVideoHash *uint64
	VideoAudioHash *uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type MessageReactions struct {
	MessageID int
	ChatID    int64
	UserID    int64
	Reactions []tg.ReactionType
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ListMessagesWithReactionCountOptions struct {
	ChatID            int64
	StartingMessageID int
	MinReactions      int
	ExcludeReactions  [2]string
}

type Storage interface {
	UpsertMessage(ctx context.Context, msg Message) error
	GetFirstMatchingMessageByImageHash(ctx context.Context, chatID int64, hash uint64, hdist int) (*Message, error)
	GetLastMatchingMessageByImageHash(ctx context.Context, chatID int64, hash uint64, hdist int) (*Message, error)
	GetFirstMatchingMessageByVideoHash(ctx context.Context, chatID int64, videoHash, audioHash uint64, hdist int) (*Message, error)
	GetLastMatchingMessageByVideoHash(ctx context.Context, chatID int64, videoHash, audioHash uint64, hdist int) (*Message, error)
	GetMessage(ctx context.Context, chatID int64, messageID int) (*Message, error)
	GetFirstChatMessageID(ctx context.Context, chatID int64) (int, error)

	UpsertMessageReactions(ctx context.Context, msg MessageReactions) error
	ListMessagesWithReactionCount(ctx context.Context, opts ListMessagesWithReactionCountOptions) ([]Message, error)

	SetLastUpdateID(ctx context.Context, lastUpdateID int) error
	GetLastUpdateID(ctx context.Context) (int, error)

	CreateTopkek(ctx context.Context, tk Topkek) (int64, error)
	UpdateTopkekStatus(ctx context.Context, id int64, status TopkekStatus) error
	GetLastTopkek(ctx context.Context, chatID int64) (*Topkek, error)
	GetTopkek(ctx context.Context, topkekID int64) (*Topkek, error)
	CreateTopkekMessage(ctx context.Context, msg TopkekMessage) error
	GetTopkekMessages(ctx context.Context, topkekID int64) ([]TopkekMessage, error)
	DeleteTopkekMessages(ctx context.Context, topkekID int64) error

	UpsertChatSettings(ctx context.Context, settings ChatSettings) error
	GetChatSettings(ctx context.Context, chatID int64) (*ChatSettings, error)
	ListChatsWithAutotopkek(ctx context.Context) ([]ChatSettings, error)
}

type StorageManager interface {
	Storage

	ExecWithTx(ctx context.Context, handler func(ctx context.Context, storage Storage) error) error
}

type Assets interface {
	GetAudioMessageDeleted() []byte
	GetAudioNoRererence() []byte
	GetAudioNoRepeat() []byte
}

type TopkekStatus string

const (
	TopkekStatusStarted TopkekStatus = "started"
	TopkekStatusDone    TopkekStatus = "done"
)

type Topkek struct {
	ID        int64        `db:"id"`
	Name      string       `db:"name"`
	ChatID    int64        `db:"chat_id"`
	MessageID int          `db:"message_id"`
	AuthorID  int64        `db:"author_id"`
	Status    TopkekStatus `db:"status"`
	CreatedAt time.Time    `db:"created_at"`
}

type TopkekMessageType string

const (
	TopkekMessageTypeSrc    TopkekMessageType = "src"
	TopkekMessageTypePoll   TopkekMessageType = "poll"
	TopkekMessageTypeDst    TopkekMessageType = "dst"
	TopkekMessageTypeWinner TopkekMessageType = "win"
)

type TopkekMessage struct {
	TopkekID        int64
	ChatID          int64
	MessageID       int
	SourceMessageID int
	Type            TopkekMessageType
	Raw             tg.Message
	CreatedAt       time.Time
}

func defaultChatSettings(chatID int64) ChatSettings {
	return ChatSettings{
		ChatID:               chatID,
		MinReactions:         5,
		ImageHammingDistance: 3,
		VideoHammingDistance: 11,
	}
}

type ChatSettings struct {
	ChatID               int64 `db:"chat_id"`
	MinReactions         int   `db:"min_reactions"`
	ImageHammingDistance int   `db:"image_hamming_distance"`
	VideoHammingDistance int   `db:"video_hamming_distance"`
	IsAutoTopkek         bool  `db:"is_autotopkek"`
}
