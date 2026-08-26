export function memberIdsFrom(record) {
  const entries = Array.isArray(record)
    ? record
    : record && typeof record === "object" && Array.isArray(record.members)
      ? record.members
      : null;
  if (!entries || entries.length === 0 || entries.some((member) =>
    !member || typeof member !== "object" || Array.isArray(member) ||
    typeof member.id !== "string" || member.id === ""
  )) return null;
  const ids = new Set(entries.map((member) => member.id));
  return ids.size === entries.length ? ids : null;
}
