package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

type gooseLoggerMock struct {
}

func (gooseLoggerMock) Fatalf(format string, v ...interface{}) {
	log.Fatalf(format, v...)
}
func (gooseLoggerMock) Printf(format string, v ...interface{}) {
	// do nothing; too much spam
}

func migrateDatabaseUp(migrationsDir string, db *sql.DB) error {
	goose.SetLogger(gooseLoggerMock{})

	err := goose.Up(db, migrationsDir)
	if err != nil {
		return fmt.Errorf("unable to migrate database: %w", err)
	}

	return nil
}

type PSQLStorageManager struct {
	db *sqlx.DB
	*storage
}

func NewPSQLStorageManager(ctx context.Context,
	purl string,
	migrationsDir string,
) (*PSQLStorageManager, error) {
	db, err := sql.Open("postgres", purl)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to postgres: %w", err)
	}

	err = migrateDatabaseUp(migrationsDir, db)
	if err != nil {
		return nil, fmt.Errorf("unable to migrate db: %w", err)
	}

	dbx := sqlx.NewDb(db, "postgres")

	return &PSQLStorageManager{
		db: dbx,
		storage: &storage{
			db: dbx,
		},
	}, nil
}

func (r *PSQLStorageManager) Close() error {
	err := r.db.Close()
	if err != nil {
		return fmt.Errorf("unable to close db: %w", err)
	}
	return nil
}

func (r *PSQLStorageManager) createStorage(db querier) *storage {
	return &storage{
		db: db,
	}
}

func (r *PSQLStorageManager) ExecWithTx(ctx context.Context, handler func(ctx context.Context, storage Storage) error) error {
	tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("unable to exec in tx: %w", err)
	}

	defer func() {
		if err != nil {
			rerr := tx.Rollback()
			if rerr != nil {
				err = errors.Join(err, fmt.Errorf("rollback error: %w", err))
			}
		}
	}()
	defer func() {
		val := recover()
		if val != nil {
			perr, ok := val.(error)
			if !ok {
				perr = fmt.Errorf("%v", val)
			}
			err = fmt.Errorf("panic: %w", perr)
		}
	}()

	storage := r.createStorage(tx)

	err = handler(ctx, storage)
	if err != nil {
		return fmt.Errorf("tx handler error: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit error: %w", err)
	}

	return nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
	SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

type storage struct {
	db querier
}

func (r *storage) getNewMessageID(ctx context.Context) (int64, error) {
	var id int64

	err := r.db.GetContext(ctx, &id, "select nextval('message_id_seq')")
	if err != nil {
		return 0, fmt.Errorf("unable to select next message id: %w", err)
	}

	return id, nil
}

func uint64PtrToInt64Ptr(v *uint64) *int64 {
	if v == nil {
		return nil
	}
	res := int64(*v)
	return &res
}

func int64PtrToUint64Ptr(v *int64) *uint64 {
	if v == nil {
		return nil
	}
	res := uint64(*v)
	return &res
}

func (r *storage) UpsertMessage(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg.Raw)
	if err != nil {
		return fmt.Errorf("unable to marshal raw message: %w", err)
	}

	nextId, err := r.getNewMessageID(ctx)
	if err != nil {
		return err
	}

	var actualId int64

	err = r.db.GetContext(ctx, &actualId, `
insert into message(
	id,
	chat_id,
	message_id,
	data,
	image_hash,
	video_video_hash,
	video_audio_hash,
	created_at,
	updated_at
) values (
	$1,
	$2,
	$3,
	$4,
	$5,
	$6,
	$7,
	$8,
	$9
)
on conflict (chat_id, message_id)
	do update 
		set 
			data = excluded.data,
			image_hash = excluded.image_hash, 
			video_video_hash = excluded.video_video_hash, 
			video_audio_hash = excluded.video_audio_hash, 
			updated_at = excluded.updated_at
returning id
	`,
		nextId,
		msg.ChatID,
		msg.MessageID,
		string(data),
		uint64PtrToInt64Ptr(msg.ImageHash),
		uint64PtrToInt64Ptr(msg.VideoVideoHash),
		uint64PtrToInt64Ptr(msg.VideoAudioHash),
		msg.CreatedAt,
		msg.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("unable to upsert message: %w", err)
	}

	if nextId != actualId {
		slog.InfoContext(ctx, "overriting message",
			slog.Int64("id", actualId),
			slog.Int64("chat_id", msg.ChatID),
			slog.Int("message_id", msg.MessageID),
		)
	}

	return nil
}

type messageDB struct {
	ChatID         int64     `db:"chat_id"`
	MessageID      int       `db:"message_id"`
	Raw            string    `db:"data"`
	ImageHash      *int64    `db:"image_hash"`
	VideoVideoHash *int64    `db:"video_video_hash"`
	VideoAudioHash *int64    `db:"video_audio_hash"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

func messagesFromDB(r []messageDB) ([]Message, error) {
	res := make([]Message, 0, len(r))

	for _, m := range r {
		mr, err := messageFromDB(m)
		if err != nil {
			return nil, err
		}
		res = append(res, *mr)
	}

	return res, nil
}

func messageFromDB(r messageDB) (*Message, error) {
	var data tgbotapi.Message
	err := json.Unmarshal([]byte(r.Raw), &data)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal raw msg: %w", err)
	}

	return &Message{
		ChatID:         r.ChatID,
		MessageID:      r.MessageID,
		Raw:            data,
		ImageHash:      int64PtrToUint64Ptr(r.ImageHash),
		VideoVideoHash: int64PtrToUint64Ptr(r.VideoVideoHash),
		VideoAudioHash: int64PtrToUint64Ptr(r.VideoAudioHash),
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}, nil
}

func (r *storage) getMatchingMessageByImageHash(ctx context.Context, chatID int64, hash uint64, hdist int, order string) (*Message, error) {
	var res []messageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	chat_id,
	message_id,
	data,
	image_hash,
	video_video_hash,
	video_audio_hash,
	created_at,
	updated_at
from message
where image_hash <@ ($1, $2)
	and image_hash is not null
	and chat_id = $3
order by created_at `+order+` 
limit 1
`,
		int64(hash),
		hdist,
		chatID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select message by image hash: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return messageFromDB(res[0])
}

func (r *storage) GetFirstMatchingMessageByImageHash(ctx context.Context, chatID int64, hash uint64, hdist int) (*Message, error) {
	return r.getMatchingMessageByImageHash(ctx, chatID, hash, hdist, "asc")
}

func (r *storage) GetLastMatchingMessageByImageHash(ctx context.Context, chatID int64, hash uint64, hdist int) (*Message, error) {
	return r.getMatchingMessageByImageHash(ctx, chatID, hash, hdist, "desc")
}

func (r *storage) GetMessage(ctx context.Context, chatID int64, messageID int) (*Message, error) {
	var res []messageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	chat_id,
	message_id,
	data,
	image_hash,
	video_video_hash,
	video_audio_hash,
	created_at,
	updated_at
from message
where chat_id = $1
	and message_id = $2
`,
		chatID,
		messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select message by image hash: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return messageFromDB(res[0])
}

func (r *storage) GetFirstChatMessageID(ctx context.Context, chatID int64) (int, error) {
	var res []int

	err := r.db.SelectContext(ctx, &res, `
select message_id
from message
where id = (
	select min(id) 
	from message
	where chat_id = $1
)
`,
		chatID,
	)
	if err != nil {
		return 0, fmt.Errorf("unable to select message by image hash: %w", err)
	}
	if len(res) == 0 {
		return 0, &ErrNotFound{}
	}

	return res[0], nil
}

func (r *storage) SetLastUpdateID(ctx context.Context, lastUpdateID int) error {
	_, err := r.db.ExecContext(ctx, `
update last_update_id
set last_update_id = $1
	`,
		lastUpdateID,
	)
	if err != nil {
		return fmt.Errorf("unable to udpate last_update_id: %w", err)
	}

	return nil
}

func (r *storage) GetLastUpdateID(ctx context.Context) (int, error) {
	var res int

	err := r.db.GetContext(ctx, &res, `select last_update_id from last_update_id`)
	if err != nil {
		return 0, fmt.Errorf("unable to select last_update_id: %w", err)
	}

	return res, nil
}

func (r *storage) getMatchingMessageByVideoHash(ctx context.Context, chatID int64, videoHash uint64, audioHash uint64, hdist int, order string) (*Message, error) {
	var res []messageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	chat_id,
	message_id,
	data,
	image_hash,
	video_video_hash,
	video_audio_hash,
	created_at,
	updated_at
from message
where video_video_hash <@ ($1, $3)
	and video_video_hash is not null
	and video_audio_hash <@ ($2, $3)
	and video_audio_hash is not null
	and chat_id = $4
order by created_at `+order+` 
limit 1
`,
		int64(videoHash),
		int64(audioHash),
		hdist,
		chatID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select message by image hash: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return messageFromDB(res[0])
}

func (r *storage) GetFirstMatchingMessageByVideoHash(ctx context.Context, chatID int64, videoHash, audioHash uint64, hdist int) (*Message, error) {
	return r.getMatchingMessageByVideoHash(ctx, chatID, videoHash, audioHash, hdist, "asc")
}

func (r *storage) GetLastMatchingMessageByVideoHash(ctx context.Context, chatID int64, videoHash, audioHash uint64, hdist int) (*Message, error) {
	return r.getMatchingMessageByVideoHash(ctx, chatID, videoHash, audioHash, hdist, "desc")
}

func (r *storage) CreateTopkek(ctx context.Context, tk Topkek) (int64, error) {
	var id int64

	err := r.db.GetContext(ctx, &id, `
insert into topkek(
	name,
	chat_id,
	message_id,
	author_id,
	created_at,
	status
) values (
	$1,
	$2,
	$3,
	$4,
	$5,
	$6
)
returning id
	`,
		tk.Name,
		tk.ChatID,
		tk.MessageID,
		tk.AuthorID,
		tk.CreatedAt,
		tk.Status,
	)
	if err != nil {
		return 0, fmt.Errorf("unable to insert topkek: %w", err)
	}

	return id, nil
}

func (r *storage) UpdateTopkekStatus(ctx context.Context, id int64, status TopkekStatus) error {
	_, err := r.db.ExecContext(ctx, `
update topkek 
set status = $2
where id = $1
	`,
		id,
		status,
	)
	if err != nil {
		return fmt.Errorf("unable to update topkek status: %w", err)
	}

	return nil
}

func (r *storage) GetLastTopkek(ctx context.Context, chatID int64) (*Topkek, error) {
	var res []Topkek

	err := r.db.SelectContext(ctx, &res, `
select 
	id,
	name,
	chat_id,
	message_id,
	author_id,
	created_at,
	status
from topkek
where chat_id = $1
order by id desc
limit 1
for update
`,
		chatID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select topkek: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return &res[0], nil
}

func (r *storage) GetTopkek(ctx context.Context, topkekID int64) (*Topkek, error) {
	var res []Topkek

	err := r.db.SelectContext(ctx, &res, `
select 
	id,
	name,
	chat_id,
	message_id,
	author_id,
	created_at,
	status
from topkek
where id = $1
`,
		topkekID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select topkek: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return &res[0], nil
}

func (r *storage) CreateTopkekMessage(ctx context.Context, msg TopkekMessage) error {
	raw, err := json.Marshal(msg.Raw)
	if err != nil {
		return fmt.Errorf("unable to marshal raw msg: %w", err)
	}

	var id int64

	err = r.db.GetContext(ctx, &id, `
insert into topkek_message(
	topkek_id,
	chat_id,
	message_id,
	source_message_id,
	type,
	raw,
	created_at
) values (
	$1,
	$2,
	$3,
	$4,
	$5,
	$6,
	$7
)
returning id
	`,
		msg.TopkekID,
		msg.ChatID,
		msg.MessageID,
		msg.SourceMessageID,
		string(msg.Type),
		string(raw),
		msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("unable to insert topkek message: %w", err)
	}

	return nil
}

type topkekMessageDB struct {
	TopkekID        int64     `db:"topkek_id"`
	ChatID          int64     `db:"chat_id"`
	MessageID       int       `db:"message_id"`
	SourceMessageID int       `db:"source_message_id"`
	Type            string    `db:"type"`
	Raw             string    `db:"raw"`
	CreatedAt       time.Time `db:"created_at"`
}

func topkekMessageFromDB(r topkekMessageDB) (*TopkekMessage, error) {
	var data tgbotapi.Message
	err := json.Unmarshal([]byte(r.Raw), &data)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal raw msg: %w", err)
	}

	return &TopkekMessage{
		TopkekID:        r.TopkekID,
		ChatID:          r.ChatID,
		MessageID:       r.MessageID,
		SourceMessageID: r.SourceMessageID,
		Type:            TopkekMessageType(r.Type),
		Raw:             data,
		CreatedAt:       r.CreatedAt,
	}, nil
}

func topkekMessagesFromDB(r []topkekMessageDB) ([]TopkekMessage, error) {
	res := make([]TopkekMessage, 0, len(r))

	for _, msg := range r {
		rr, err := topkekMessageFromDB(msg)
		if err != nil {
			return nil, err
		}
		res = append(res, *rr)
	}

	return res, nil
}

func (r *storage) GetTopkekMessages(ctx context.Context, topkekID int64) ([]TopkekMessage, error) {
	var res []topkekMessageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	topkek_id,
	chat_id,
	message_id,
	source_message_id,
	type,
	raw,
	created_at
from topkek_message
where topkek_id = $1
order by id
`,
		topkekID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select topkek messages: %w", err)
	}

	return topkekMessagesFromDB(res)
}

func (r *storage) GetTopkekWinners(ctx context.Context, from time.Time) ([]TopkekMessage, error) {
	var res []topkekMessageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	tm.topkek_id,
	tm.chat_id,
	tm.message_id,
	tm.source_message_id,
	tm.type,
	tm.raw,
	tm.created_at
from topkek_message as tm
inner join topkek as t
	on t.id = tm.topkek_id
where t.created_at > $1
	and tm.type = $2
order by tm.id
`,
		from,
		TopkekMessageTypeWinner,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select topkek winner messages: %w", err)
	}

	return topkekMessagesFromDB(res)
}

func (r *storage) DeleteTopkekMessages(ctx context.Context, topkekID int64) error {
	_, err := r.db.ExecContext(ctx, `
delete from topkek_message
where topkek_id = $1
`,
		topkekID,
	)
	if err != nil {
		return fmt.Errorf("unable to select topkek messages: %w", err)
	}

	return nil
}

func (r *storage) UpsertMessageReactions(ctx context.Context, msg MessageReactions) error {
	reactions, err := json.Marshal(msg.Reactions)
	if err != nil {
		return fmt.Errorf("unable to marshal reactions: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
insert into message_reactions(
	chat_id,
	message_id,
	user_id,
	reactions,
	created_at,
	updated_at
) values (
	$1,
	$2,
	$3,
	$4,
	$5,
	$6
)
on conflict (chat_id, message_id, user_id)
	do update 
		set 
			reactions = excluded.reactions,
			updated_at = excluded.updated_at
	`,
		msg.ChatID,
		msg.MessageID,
		msg.UserID,
		string(reactions),
		msg.CreatedAt,
		msg.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("unable to upsert message reactions: %w", err)
	}

	return nil
}

func (r *storage) ListMessagesWithReactionCount(ctx context.Context, opts ListMessagesWithReactionCountOptions) ([]Message, error) {
	var res []messageDB

	err := r.db.SelectContext(ctx, &res, `
select 
	m.chat_id,
	m.message_id,
	m.data,
	m.image_hash,
	m.video_video_hash,
	m.video_audio_hash,
	m.created_at,
	m.updated_at
from message as m
inner join lateral (
	SELECT chat_id, message_id, 
		sum(coalesce(jsonb_array_length(reactions), 0)) reacts, 
      	sum(
           coalesce((SELECT sum((case 
							when (elem->>'emoji') IN ($1, $2) 
								then 1
							else 0
					end))
           FROM jsonb_array_elements(reactions) AS elem
		   group by chat_id, message_id), 0)
       	) AS is_stale
	FROM message_reactions as mr
	where mr.chat_id = m.chat_id
		and mr.message_id = m.message_id
	group by mr.chat_id, mr.message_id
) as mr
	on mr.is_stale = 0
		and mr.reacts >= $3
where m.chat_id = $4
	and m.id >= (select min(id) from message where chat_id = $4 and message_id > $5)
	and (m.image_hash is not null
		or (m.video_video_hash is not null
			and m.video_audio_hash is not null))
order by m.id
`,
		opts.ExcludeReactions[0],
		opts.ExcludeReactions[1],
		opts.MinReactions,
		opts.ChatID,
		opts.StartingMessageID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select message with min reactions: %w", err)
	}

	return messagesFromDB(res)
}

func (r *storage) UpsertChatSettings(ctx context.Context, settings ChatSettings) error {
	_, err := r.db.ExecContext(ctx, `
insert into chat_settings(
	chat_id,
	min_reactions,
	image_hamming_distance,
	video_hamming_distance,
	is_autotopkek
) values (
	$1,
	$2,
	$3,
	$4,
	$5
)
on conflict (chat_id)
	do update 
		set 
			min_reactions = excluded.min_reactions,
			image_hamming_distance = excluded.image_hamming_distance,
			video_hamming_distance = excluded.video_hamming_distance,
			is_autotopkek = excluded.is_autotopkek
	`,
		settings.ChatID,
		settings.MinReactions,
		settings.ImageHammingDistance,
		settings.VideoHammingDistance,
		settings.IsAutoTopkek,
	)
	if err != nil {
		return fmt.Errorf("unable to upsert chat settings: %w", err)
	}

	return nil
}

func (r *storage) GetChatSettings(ctx context.Context, chatID int64) (*ChatSettings, error) {
	var res []ChatSettings

	err := r.db.SelectContext(ctx, &res, `
select 
	chat_id,
	min_reactions,
	image_hamming_distance,
	video_hamming_distance,
	is_autotopkek
from chat_settings
where chat_id = $1
`,
		chatID,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select chat settings: %w", err)
	}

	if len(res) == 0 {
		return nil, &ErrNotFound{}
	}

	return &res[0], nil
}

func (r *storage) ListChatsWithAutotopkek(ctx context.Context) ([]ChatSettings, error) {
	var res []ChatSettings

	err := r.db.SelectContext(ctx, &res, `
select 
	chat_id,
	min_reactions,
	image_hamming_distance,
	video_hamming_distance,
	is_autotopkek
from chat_settings
where is_autotopkek
`,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to select chat settings: %w", err)
	}

	return res, nil
}
