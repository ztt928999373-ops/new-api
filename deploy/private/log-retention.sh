#!/usr/bin/env bash
set -Eeuo pipefail
set +x

readonly log_dir=/data/logs
readonly max_age_minutes=43200
readonly max_bytes=209715200

[[ -d $log_dir && ! -L $log_dir ]] || { echo 'Expected a real /data/logs directory.' >&2; exit 1; }
[[ $(realpath -e -- "$log_dir") == "$log_dir" ]] || { echo 'Refusing unexpected log path.' >&2; exit 1; }

expired_count=$(find "$log_dir" -xdev -type f -mmin "+$max_age_minutes" -printf x | wc -c)
find "$log_dir" -xdev -type f -mmin "+$max_age_minutes" -delete

bytes=$(du -sb "$log_dir" | awk '{print $1}')
removed_for_size=0
if (( bytes > max_bytes )); then
  while IFS= read -r -d '' entry; do
    path=${entry#* }
    [[ $path == "$log_dir/"* && -f $path && ! -L $path ]] || continue
    rm -- "$path"
    removed_for_size=$((removed_for_size + 1))
    bytes=$(du -sb "$log_dir" | awk '{print $1}')
    (( bytes <= max_bytes )) && break
  done < <(find "$log_dir" -xdev -type f -printf '%T@ %p\0' | sort -z -n)
fi

(( bytes <= max_bytes )) || { echo 'Log directory exceeds the 200 MiB cap after pruning.' >&2; exit 1; }
printf 'LOG_RETENTION=PASS expired_files=%s size_pruned_files=%s bytes=%s max_age_days=30 max_bytes=%s\n' \
  "$expired_count" "$removed_for_size" "$bytes" "$max_bytes"
