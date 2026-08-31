-- Reference schema for the partner directory sync.
--
-- The unique index on directory_id is not a nicety: it is what makes picker
-- creation idempotent under concurrency. The ErrNotFound check in the
-- reconciler is a fast path, not a guarantee — two workers can both miss it.

create table pickers (
  id            bigserial   primary key,   -- this value is the picker_id written back
  directory_id  text        not null,      -- the ONLY match key. never login, never name
  login         text        not null,      -- alias published by the directory (@separador.app)
  display_name  text        not null,      -- abbreviated name, e.g. "Marcio C."
  enabled       boolean     not null default true,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),

  constraint pickers_directory_id_key unique (directory_id)
);

create index pickers_enabled_idx on pickers (enabled) where enabled;

-- One row per partner directory. checkpoint holds the newest
-- meta.lastModified seen in a cycle that completed end to end.
create table directory_sync_state (
  partner    text        primary key,
  checkpoint timestamptz not null default 'epoch',
  updated_at timestamptz not null default now()
);

insert into directory_sync_state (partner) values ('helppi')
  on conflict (partner) do nothing;

-- Optional: a single-writer guard, if the worker runs in a replicated
-- deployment. Take it for the duration of a cycle and skip the cycle when the
-- lock is already held.
--
--   select pg_try_advisory_lock(hashtext('directory_sync:helppi'));
--   ...
--   select pg_advisory_unlock(hashtext('directory_sync:helppi'));
