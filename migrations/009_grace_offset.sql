-- +goose Up
-- +goose StatementBegin

alter table chat_settings add column is_grace_offset boolean not null default false;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
select 1;
-- +goose StatementEnd
