-- +goose Up
-- +goose StatementBegin

create table setting_prompt (
    chat_id bigint not null,
    message_id bigint not null,
    user_id bigint not null,
    setting text not null,
    created_at timestamp not null,
    primary key (chat_id, message_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
select 1;
-- +goose StatementEnd
