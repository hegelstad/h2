// Auto-installed by EnsureConfigDir into <OPENCODE_CONFIG_DIR>/plugins/.
// Bridges opencode plugin events to `h2 handle-hook` so the harness can
// drive Active/Idle from session.idle rather than scraping the TUI.
export const H2Bridge = async ({ $ }) => {
  const agent = process.env.H2_AGENT_NAME ?? "";
  const hook = (evt: string) =>
    $`h2 handle-hook --agent ${agent} --event ${evt}`.quiet().nothrow();
  return {
    event: async ({ event }) => {
      if (event.type === "session.idle") await hook("opencode.session.idle");
      else if (event.type === "message.updated") await hook("opencode.session.active");
      else if (event.type === "permission.asked") await hook("opencode.session.active");
      else if (event.type === "session.error") await hook("opencode.session.error");
    },
  };
};
