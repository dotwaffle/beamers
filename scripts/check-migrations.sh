#!/bin/sh

set -eu

migration_check_dir=$(mktemp -d)
trap 'rm -r "$migration_check_dir"' EXIT HUP INT TERM

cp internal/store/migrations/atlas.sum internal/store/migrations/*.sql "$migration_check_dir/"

atlas migrate validate \
  --dir "file://$migration_check_dir" \
  --dev-url 'sqlite://file?mode=memory&_fk=1'

atlas migrate diff schema-parity \
  --dir "file://$migration_check_dir" \
  --to ent://ent/schema \
  --dev-url 'sqlite://file?mode=memory&_fk=1'

if find "$migration_check_dir" -type f -name '*_schema-parity.sql' -print -quit | grep -q .; then
  echo 'committed migrations differ from the Ent schema' >&2
  exit 1
fi
