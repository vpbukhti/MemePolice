-- +goose Up
-- +goose StatementBegin

alter table chat_settings add column is_autotopkek boolean not null default false;

alter table topkek_message add column created_at timestamp not null default now();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
select 1;
-- +goose StatementEnd