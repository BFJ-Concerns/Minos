# Shared cgroup memory access for the run-body scripts. Sourced, never
# executed: one resolution of this run's cgroup and one read of its memory
# counters, so the pressure watch, the terminal death evidence and the
# telemetry snapshot cannot drift apart. Collected values are residue —
# nothing here reads them back to steer a run.

# Sets cgroup_dir to this run's cgroup: the MINOS_CGROUP_DIR override when
# set, otherwise the path this process reports under the unified hierarchy.
resolve_cgroup_dir() {
  if [ -n "${MINOS_CGROUP_DIR:-}" ]; then
    cgroup_dir="$MINOS_CGROUP_DIR"
  else
    cgroup_path="$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -n 1)"
    cgroup_dir="/sys/fs/cgroup${cgroup_path}"
  fi
}

# Reads the counters both consumers judge and report on, from the resolved
# cgroup_dir: anonymous footprint and swap (the honest memory figure —
# memory.current counts page cache and is never one), PSI stall totals, and
# the workingset refaults. An absent or unreadable counter comes back empty
# rather than failing: the watch disables itself on one and the snapshot
# omits it, each in its own way.
read_cgroup_memory_counters() {
  cgroup_anon="$(awk '$1 == "anon" { print $2; exit }' "$cgroup_dir/memory.stat" 2>/dev/null || true)"
  cgroup_refault_anon="$(awk '$1 == "workingset_refault_anon" { print $2; exit }' "$cgroup_dir/memory.stat" 2>/dev/null || true)"
  cgroup_refault_file="$(awk '$1 == "workingset_refault_file" { print $2; exit }' "$cgroup_dir/memory.stat" 2>/dev/null || true)"
  cgroup_swap="$(cat "$cgroup_dir/memory.swap.current" 2>/dev/null || echo "")"
  cgroup_stall_some_us="$(awk '$1 == "some" { sub("total=", "", $NF); print $NF; exit }' "$cgroup_dir/memory.pressure" 2>/dev/null || true)"
  cgroup_stall_full_us="$(awk '$1 == "full" { sub("total=", "", $NF); print $NF; exit }' "$cgroup_dir/memory.pressure" 2>/dev/null || true)"
}
