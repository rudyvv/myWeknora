type SourceCadence = { id: string; scan_interval_minutes: number }

// Refresh clean drafts from the server while keeping a user's unsaved choice.
export function reconcileScanCadenceDrafts(
  drafts: Record<string, number>,
  previous: readonly SourceCadence[],
  incoming: readonly SourceCadence[],
): void {
  const previousByID = new Map(previous.map(source => [source.id, source.scan_interval_minutes]))
  const incomingIDs = new Set<string>()
  for (const source of incoming) {
    incomingIDs.add(source.id)
    if (!(source.id in drafts) || drafts[source.id] === previousByID.get(source.id)) {
      drafts[source.id] = source.scan_interval_minutes
    }
  }
  for (const id of Object.keys(drafts)) {
    if (!incomingIDs.has(id)) delete drafts[id]
  }
}
