-- +goose Up
-- +goose StatementBegin

update topkek_message
set created_at = t.created_at
from topkek as t
where topkek_message.topkek_id = t.id;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
select 1;
-- +goose StatementEnd