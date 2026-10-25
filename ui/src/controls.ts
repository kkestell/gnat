import type { Action, Snapshot } from "./api";

export function activeTurn(snapshot: Snapshot) {
  return snapshot.turns.find(
    (t) => t.agent_id === snapshot.orchestrator.id && t.state === "running",
  );
}

export function canAct(snapshot: Snapshot, action: Action): boolean {
  switch (action) {
    case "pause":
      return snapshot.scheduling === "running";
    case "resume":
      return (
        snapshot.scheduling === "paused" || snapshot.scheduling === "stopped"
      );
    case "stop":
      return snapshot.scheduling !== "stopped";
    case "interrupt":
      return !!activeTurn(snapshot);
    case "retry":
      return (
        snapshot.scheduling === "running" &&
        snapshot.orchestrator.lifecycle === "error" &&
        !activeTurn(snapshot)
      );
    default:
      return true;
  }
}
