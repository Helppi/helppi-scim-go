-- The unique index on directory_id is not a nicety: it is what makes creation
-- idempotent under concurrency. The reconciler's "does it exist?" lookup is a
-- fast path, not a guarantee — two workers can both miss it.
create table if not exists helppers (
    id           bigserial   primary key,   -- written back to the directory as externalId
    directory_id text        not null,      -- the ONLY key the two sides match on
    login        text        not null,      -- alias published by the directory
    display_name text        not null,      -- abbreviated name, e.g. "Marcio C."
    enabled      boolean     not null default true,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),

    constraint helppers_directory_id_key unique (directory_id)
);

create index if not exists helppers_enabled_idx on helppers (enabled) where enabled;

-- One row per partner directory. checkpoint holds the newest meta.lastModified
-- from a cycle that completed end to end. timestamptz matters: it is compared
-- against timestamps issued by the directory, so storing it without a zone
-- reintroduces exactly the clock problem the design removes.
create table if not exists directory_sync_state (
    partner    text        primary key,
    checkpoint timestamptz not null default 'epoch',
    updated_at timestamptz not null default now()
);
