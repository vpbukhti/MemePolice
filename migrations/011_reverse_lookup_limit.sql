-- +goose Up
-- +goose StatementBegin

alter table chat_settings add column reverse_lookup_limit int not null default 3;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
select 1;
-- +goose StatementEnd
