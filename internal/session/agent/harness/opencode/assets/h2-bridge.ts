// Auto-installed by EnsureConfigDir into <OPENCODE_CONFIG_DIR>/plugins/.
// Bridges opencode plugin events to `h2 handle-hook` so the harness can
// drive Active/Idle from session.idle rather than scraping the TUI.
//
// Do not subscribe to per-message update events (high frequency; fills the mailbox).
// session.created / session.status / permission.asked are enough for Active.
export const H2Bridge = async ({ $ }) => {
  const agent = process.env.H2_AGENT_NAME ?? "";
  const bin = process.env.H2_BIN || "h2";
  const hook = (evt: string) =>
    $`${bin} handle-hook --agent ${agent} --event ${evt}`.quiet().nothrow();
  return {
    event: async ({ event }) => {
      try {
        if (!agent) return;
        const typ = event?.type;
        if (!typ) return;
        if (typ === "session.idle") await hook("opencode.session.idle");
        else if (typ === "permission.asked") await hook("opencode.permission.asked");
        else if (typ === "session.error") await hook("opencode.session.error");
        else if (typ === "session.created" || typ === "session.status")
          await hook("opencode.session.active");
      } catch {
        // Never throw: a plugin exception can disable idle delivery.
      }
    },
  };
};
